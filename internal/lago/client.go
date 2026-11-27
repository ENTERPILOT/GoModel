package lago

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// maxBatchSize is the most events Lago takes in one POST /events/batch.
const maxBatchSize = 100

// maxErrorBody caps how much of an error response is read. A rejected batch
// of maxBatchSize events lists each of them in a few dozen bytes.
const maxErrorBody = 64 << 10

// maxDrainBody caps how much of a successful response is read and discarded.
// Lago repeats the batch in it, which can pass maxErrorBody; a body read to
// its end lets the connection be reused.
const maxDrainBody = 4 << 20

// duplicateTransaction is the error Lago gives an event whose transaction_id
// it already holds.
const duplicateTransaction = "value_already_exist"

// client posts event batches to one Lago API.
type client struct {
	batchURL string
	apiKey   string
	http     *http.Client
}

func newClient(apiURL, apiKey string, httpClient *http.Client) *client {
	return &client{
		batchURL: strings.TrimRight(strings.TrimSpace(apiURL), "/") + "/events/batch",
		apiKey:   strings.TrimSpace(apiKey),
		http:     httpClient,
	}
}

// send posts one batch and returns Lago's status with the body of an error
// response. err is set when no response arrived.
func (c *client) send(ctx context.Context, events []Event) (status int, body []byte, err error) {
	payload, err := json.Marshal(struct {
		Events []Event `json:"events"`
	}{events})
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.batchURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Drained so the connection is reused; the answer repeats the events.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBody))
		return resp.StatusCode, nil, nil
	}
	body, _ = io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return resp.StatusCode, body, nil
}

// rejectsBatch reports whether a status means Lago, or a proxy in front of
// it, refuses this batch for what it contains, so sending it again cannot
// succeed: a malformed body, a body too large, or a failed validation. Any
// other failure (a rejected key, a wrong path, throttling, a server error)
// is fixed outside the batch, which is kept and retried.
func rejectsBatch(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// rejectedEvents reads which events of a batch of size n a validation error
// names. Lago validates a batch as a whole and lists the failing events in
// error_details by their index in the request. duplicate marks the ones it
// refused only because it already holds them.
func rejectedEvents(body []byte, n int) (rejected map[int]bool, duplicates int) {
	rejected = make(map[int]bool)
	gjson.GetBytes(body, "error_details").ForEach(func(key, value gjson.Result) bool {
		index, err := strconv.Atoi(key.String())
		if err != nil || index < 0 || index >= n || rejected[index] {
			return true
		}
		rejected[index] = true
		if strings.Contains(value.Raw, duplicateTransaction) {
			duplicates++
		}
		return true
	})
	return rejected, duplicates
}
