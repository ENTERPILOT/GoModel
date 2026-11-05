package storage

import "github.com/google/uuid"

// processWriterID names this gateway process in the audit and usage rows it
// persists. It is fixed for the life of the process, so every configuration
// generation a reload builds shares it.
var processWriterID = uuid.NewString()

// WriterID returns the identifier this process stamps on the audit and usage
// rows it writes. Another replica's live log view uses it to tell the rows it
// wrote itself, which it already streamed, from the rows of its peers.
func WriterID() string {
	return processWriterID
}

// RecentWrite names one persisted row and when it was written: the writer's
// clock at the batch write that stored it, in unix microseconds.
type RecentWrite struct {
	ID        string
	WrittenAt int64
}

// WriteStamp marks the batch write that persisted an audit or usage entry:
// the writing process (WriterID) and its clock in unix microseconds. Entries
// carry a pointer to it so an unstamped entry costs one word; the MongoDB
// stores inline it as the writer_id and written_at fields.
type WriteStamp struct {
	WriterID  string `bson:"writer_id,omitempty"`
	WrittenAt int64  `bson:"written_at,omitempty"`
}

// SQLValues returns the writer_id and written_at column values, both NULL for
// an unstamped entry so it stays out of the partial written_at index.
func (s *WriteStamp) SQLValues() (writerID, writtenAt any) {
	if s == nil || s.WriterID == "" || s.WrittenAt == 0 {
		return nil, nil
	}
	return s.WriterID, s.WrittenAt
}
