package labeler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/beatlabs/github-auth/app"
	"github.com/coder/labeler/ghapi"
	"github.com/google/go-github/v59/github"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/api/iterator"
)

type Indexer struct {
	Log           *slog.Logger
	OpenAI        *openai.Client
	AppConfig     *app.Config
	BigQuery      *bigquery.Client
	IndexInterval time.Duration
}

func (s *Indexer) findRandInstall(ctx context.Context) (*github.Installation, error) {
	client := github.NewClient(s.AppConfig.Client())
	installations, err := ghapi.Page[*github.Installation](
		ctx,
		client,
		func(ctx context.Context, opt *github.ListOptions) ([]*github.Installation, *github.Response, error) {
			return client.Apps.ListInstallations(ctx, opt)
		},
		1e6,
	)
	if err != nil {
		return nil, fmt.Errorf("list installations: %w", err)
	}

	// We get a random installation because we have no guarantee
	// the labeler process will run for a long time and we want
	// to fairly index all organizations. This
	// avoids having to store some kind of index state.
	toIndex := installations[rand.Intn(len(installations))]
	return toIndex, nil
}

const embeddingDimensions = 256

func f32to64(f []float32) []float64 {
	out := make([]float64, len(f))
	for i, v := range f {
		out[i] = float64(v)
	}
	return out
}

func (s *Indexer) embedIssue(ctx context.Context, issue *github.Issue) ([]float64, error) {
	var buf strings.Builder
	fmt.Fprintf(&buf, "Title: %s\n", issue.GetTitle())
	fmt.Fprintf(&buf, "State: %s\n", issue.GetState())
	fmt.Fprintf(&buf, "Author: %s\n", issue.GetUser().GetLogin())
	var labelNames []string
	for _, label := range issue.Labels {
		labelNames = append(labelNames, label.GetName())
	}
	fmt.Fprintf(&buf, "Labels: %s\n", strings.Join(labelNames, ", "))
	fmt.Fprintf(&buf, "Body: %s\n", issue.GetBody())

	tokens := tokenize(buf.String())
	if len(tokens) > 8191 {
		tokens = tokens[:8191]
	}
	resp, err := s.OpenAI.CreateEmbeddings(
		ctx,
		&openai.EmbeddingRequestStrings{
			Model:      openai.SmallEmbedding3,
			Input:      []string{strings.Join(tokens, "")},
			Dimensions: embeddingDimensions,
		},
	)
	if err != nil {
		return nil, err
	}

	if len(resp.Data) != 1 {
		return nil, fmt.Errorf("expected 1 embedding, got %d", len(resp.Data))
	}

	return f32to64(resp.Data[0].Embedding), nil
}

func (s *Indexer) issuesTable() *bigquery.Table {
	return s.BigQuery.Dataset("ghindex").Table("issues")
}

// getUpdatedAts helps avoid duplicate inserts by letting the caller skip over
// issues that have already been indexed.
func (s *Indexer) getUpdatedAts(ctx context.Context, installID int64) (map[int64]time.Time, error) {
	queryStr := `
	WITH RankedIssues AS (
	  SELECT
		id,
		updated_at,
		inserted_at,
		ROW_NUMBER() OVER (PARTITION BY inserted_at, id ORDER BY inserted_at DESC) AS rn
	  FROM
		` + "`coder-labeler.ghindex.issues`" + `
	  WHERE install_id = @install_id
	)
	SELECT
	  id,
	  updated_at
	FROM
	  RankedIssues
	WHERE
	  rn = 1
	ORDER BY
	  inserted_at DESC;
	`

	q := s.BigQuery.Query(queryStr)
	q.Parameters = []bigquery.QueryParameter{
		{
			Name:  "install_id",
			Value: installID,
		},
	}

	job, err := q.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("run query: %w", err)
	}
	iter, err := job.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read query: %w", err)
	}

	issues := make(map[int64]time.Time)
	for {
		var i BqIssue
		err := iter.Next(&i)
		if err == iterator.Done {
			break
		}
		if err != nil {
			s.Log.Error("read issue", "error", err)
			break
		}
		issues[i.ID] = i.UpdatedAt
	}
	return issues, nil
}

// indexInstall indexes all the issues for an installation.
func (s *Indexer) indexInstall(ctx context.Context, install *github.Installation) error {
	idstr := fmt.Sprintf("%d", install.GetID())

	config, err := s.AppConfig.InstallationConfig(idstr)
	if err != nil {
		return fmt.Errorf("get installation config: %w", err)
	}

	client := github.NewClient(config.Client(ctx))

	// List all repos
	repos, err := ghapi.Page(ctx,
		client,
		func(ctx context.Context, opt *github.ListOptions) ([]*github.Repository, *github.Response, error) {
			lr, resp, err := client.Apps.ListRepos(ctx, opt)
			if err != nil {
				return nil, resp, fmt.Errorf("list repos: %w", err)
			}
			return lr.Repositories, resp, nil
		},
		-1,
	)
	if err != nil {
		return fmt.Errorf("list repos: %w", err)
	}

	log := s.Log.With("install", install.GetID())
	log.Debug("indexing install", "repos", len(repos))

	table := s.issuesTable()
	inserter := table.Inserter()

	cachedIssues, err := s.getUpdatedAts(ctx, install.GetID())
	if err != nil {
		return fmt.Errorf("get cached issues: %w", err)
	}
	log.Debug("got cached issues", "count", len(cachedIssues))

	for _, repo := range repos {
		// List all issues
		issues, err := ghapi.Page(ctx,
			client,
			func(ctx context.Context, opt *github.ListOptions) ([]*github.Issue, *github.Response, error) {
				issues, resp, err := client.Issues.ListByRepo(ctx, repo.GetOwner().GetLogin(), repo.GetName(), &github.IssueListByRepoOptions{
					State:       "all",
					ListOptions: *opt,
					Sort:        "updated",
					Direction:   "asc",
				})
				return issues, resp, err
			},
			-1,
		)
		if err != nil {
			return fmt.Errorf("list issues: %w", err)
		}
		log := s.Log.With("repo", repo.GetFullName())
		log.Debug("found issues", "count", len(issues))

		for _, issue := range issues {
			if uat, ok := cachedIssues[issue.GetID()]; ok {
				if issue.UpdatedAt.Time.Equal(uat) {
					log.Debug("skipping issue due to cache", "num", issue.GetNumber())
					continue
				}
			}
			emb, err := s.embedIssue(ctx, issue)
			if err != nil {
				return fmt.Errorf("embed issue %v: %w", issue.ID, err)
			}
			err = inserter.Put(ctx, BqIssue{
				ID:          issue.GetID(),
				InstallID:   install.GetID(),
				User:        repo.GetOwner().GetLogin(),
				Repo:        repo.GetName(),
				Title:       issue.GetTitle(),
				Number:      issue.GetNumber(),
				State:       issue.GetState(),
				Body:        issue.GetBody(),
				CreatedAt:   issue.GetCreatedAt().Time,
				UpdatedAt:   issue.GetUpdatedAt().Time,
				InsertedAt:  time.Now(),
				PullRequest: issue.IsPullRequest(),
				Embedding:   emb,
			})
			if err != nil {
				return fmt.Errorf("insert issue: %w", err)
			}
			updateAge := time.Since(issue.GetUpdatedAt().Time).Truncate(time.Minute)
			log.Debug(
				"indexed issue", "num", issue.GetNumber(),
				"update_age", updateAge.String(),
			)
		}
	}
	log.Debug("finished indexing")
	return nil
}

func (s *Indexer) runIndex(ctx context.Context) error {
	install, err := s.findRandInstall(ctx)
	if err != nil {
		return fmt.Errorf("find random install: %w", err)
	}

	if err := s.indexInstall(ctx, install); err != nil {
		return fmt.Errorf("index install %v: %w", install.GetID(), err)
	}

	return nil
}

// Run starts the indexer and blocks until it's done.
func (s *Indexer) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.IndexInterval)
	s.Log.Info("indexer started", "interval", s.IndexInterval)
	defer ticker.Stop()
	for {
		err := s.runIndex(ctx)
		if err != nil {
			s.Log.Error("indexer", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			continue
		}
	}
}
