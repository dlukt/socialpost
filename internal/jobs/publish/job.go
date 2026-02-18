package publish

import "time"

const QueueKey = "queue:publish-post"

// Job represents a single account-level publish operation.
type Job struct {
	PostID     int64     `json:"post_id"`
	AccountID  int64     `json:"account_id"`
	Attempt    int       `json:"attempt"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}
