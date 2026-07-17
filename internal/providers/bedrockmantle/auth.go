package bedrockmantle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/enterpilot/gomodel/internal/providers"
)

const signingService = "bedrock"

type authTransport struct {
	base        http.RoundTripper
	keys        *providers.Keyring
	credentials aws.CredentialsProvider
	region      string
	signer      *v4.Signer
	now         func() time.Time
}

func authenticatedClient(client *http.Client, keys *providers.Keyring, credentials aws.CredentialsProvider, region string) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = &authTransport{
		base:        transport,
		keys:        keys,
		credentials: credentials,
		region:      region,
		signer:      v4.NewSigner(),
		now:         time.Now,
	}
	return &clone
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header = req.Header.Clone()

	if token := t.keys.Next(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		return t.base.RoundTrip(request)
	}
	if t.credentials == nil {
		return nil, fmt.Errorf("credentials for Bedrock Mantle are not configured")
	}

	credentials, err := t.credentials.Retrieve(req.Context())
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials: %w", err)
	}
	payloadHash, err := requestPayloadHash(req)
	if err != nil {
		return nil, err
	}
	if err := t.signer.SignHTTP(req.Context(), credentials, request, payloadHash, signingService, t.region, t.now()); err != nil {
		return nil, fmt.Errorf("sign Bedrock Mantle request: %w", err)
	}
	return t.base.RoundTrip(request)
}

func requestPayloadHash(req *http.Request) (string, error) {
	hash := sha256.New()
	if req.Body != nil {
		if req.GetBody == nil {
			return "", fmt.Errorf("sign Bedrock Mantle request: body is not replayable")
		}
		body, err := req.GetBody()
		if err != nil {
			return "", fmt.Errorf("read Bedrock Mantle request body: %w", err)
		}
		defer func() { _ = body.Close() }()
		if _, err := io.Copy(hash, body); err != nil {
			return "", fmt.Errorf("hash Bedrock Mantle request body: %w", err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
