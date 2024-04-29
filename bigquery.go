package labeler

import "time"

// BqIssue represents a GitHub issue in BigQuery.
// The schema is defined here:
// https://console.cloud.google.com/bigquery?authuser=1&folder=297399687849&organizationId=867596835188&orgonly=true&project=coder-labeler&supportedpurview=organizationId&ws=!1m5!1m4!4m3!1scoder-labeler!2sghindex!3sissues.
type BqIssue struct {
	ID          int64     `bigquery:"id"`
	InstallID   int64     `bigquery:"install_id"`
	User        string    `bigquery:"user"`
	Repo        string    `bigquery:"repo"`
	Number      int       `bigquery:"number"`
	Title       string    `bigquery:"title"`
	State       string    `bigquery:"state"`
	Body        string    `bigquery:"body"`
	CreatedAt   time.Time `bigquery:"created_at"`
	UpdatedAt   time.Time `bigquery:"updated_at"`
	InsertedAt  time.Time `bigquery:"inserted_at"`
	Embedding   []float64 `bigquery:"embedding"`
	PullRequest bool      `bigquery:"pull_request"`
}
