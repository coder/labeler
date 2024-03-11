package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/coder/labeler"
	"github.com/coder/labeler/ghapi"
	"github.com/coder/serpent"
	"github.com/google/go-github/v59/github"
)

type testStats struct {
	nIssues int

	hits []string

	// falseAdds is the number of false-adds, i.e. labels that were added but shouldn't have been.
	// These are worst than falseRms because it causes two issue events in the GitHub UI.
	// where-as falseRms only causes one.
	falseAdds []string

	// falseRms is the number of false-removes.
	falseRms []string

	tokens int
	tooks  []time.Duration
}

func (s *testStats) process(
	start time.Time,
	wantLabels []string,
	infResp *labeler.InferResponse,
) {
	s.nIssues++

	for _, label := range wantLabels {
		if !slices.Contains(infResp.SetLabels, label) {
			s.falseRms = append(s.falseRms, label)
		} else {
			s.hits = append(s.hits, label)
		}
	}
	for _, label := range infResp.SetLabels {
		if !slices.Contains(wantLabels, label) {
			s.falseAdds = append(s.falseAdds, label)
		}
	}
	s.tokens += infResp.TokensUsed
	s.tooks = append(s.tooks, time.Since(start))
}

func uniqCount(ss []string) map[string]int {
	m := make(map[string]int)
	for _, s := range ss {
		m[s]++
	}
	return m
}

type KV[Key any, Value any] struct {
	Key   Key
	Value Value
}

func topN(m map[string]int, n int) []KV[string, int] {
	var kvs []KV[string, int]
	for k, v := range m {
		kvs = append(kvs, KV[string, int]{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool {
		return kvs[i].Value > kvs[j].Value
	})
	if len(kvs) < n {
		n = len(kvs)
	}
	return kvs[:n]
}

func (kv *KV[Key, Value]) String() string {
	return fmt.Sprintf("%v: %v", kv.Key, kv.Value)
}

func (s *testStats) print(w io.Writer) error {
	twr := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)

	fmt.Fprintf(twr, "Total issues:\t%d\n", s.nIssues)
	fmt.Fprintf(twr, "False adds:\t%d\t%.2f%%\n", len(s.falseAdds), float64(len(s.falseAdds))/float64(s.nIssues)*100)
	fmt.Fprintf(twr, "False removes:\t%d\t%.2f%%\n", len(s.falseRms), float64(len(s.falseRms))/float64(s.nIssues)*100)
	fmt.Fprintf(twr, "Hits:\t%d\t%.2f%%\t%v\n", len(s.hits), float64(len(s.hits))/float64(s.nIssues)*100,
		topN(uniqCount(s.hits), 5),
	)
	fmt.Fprintf(twr, "Tokens used:\t%d\n", s.tokens)
	return twr.Flush()
}

func (r *rootCmd) testCmd() *serpent.Cmd {
	var (
		installID string
		user      string
		repo      string
		nIssues   int64
	)
	return &serpent.Cmd{
		Use:   "test",
		Short: "Test performance and accuracy of a given repo",
		Handler: func(inv *serpent.Invocation) error {
			log := newLogger()

			appConfig, err := r.appConfig()
			if err != nil {
				return err
			}

			ctx := inv.Context()

			ai, err := r.ai(ctx)
			if err != nil {
				return err
			}

			srv := &labeler.Service{
				Log:       log,
				OpenAI:    ai,
				Model:     r.openAIModel,
				AppConfig: appConfig,
			}
			srv.Init()

			instConfig, err := appConfig.InstallationConfig(installID)
			if err != nil {
				return fmt.Errorf("get installation config: %w", err)
			}

			githubClient := github.NewClient(instConfig.Client(ctx))

			testIssues, err := ghapi.Page(
				ctx,
				githubClient,
				func(ctx context.Context, opt *github.ListOptions) ([]*github.Issue, *github.Response, error) {
					log.Info("load issues page from GitHub")
					return githubClient.Issues.ListByRepo(
						ctx,
						user,
						repo,
						&github.IssueListByRepoOptions{
							State: "all",
						},
					)
				},
				int(nIssues),
			)
			if err != nil {
				return fmt.Errorf("list issues: %w", err)
			}

			var st testStats

			for i, issue := range testIssues {
				wantLabels := make([]string, 0, len(issue.Labels))
				for _, label := range issue.Labels {
					wantLabels = append(wantLabels, label.GetName())
				}

				start := time.Now()
				resp, err := srv.Infer(ctx, &labeler.InferRequest{
					InstallID: installID,
					User:      user,
					Repo:      repo,
					Issue:     issue.GetNumber(),
				})
				if err != nil {
					return fmt.Errorf("infer: %w", err)
				}
				st.process(start, wantLabels, resp)

				log.Info("processed issue",
					"i", i,
					"took", time.Since(start),
					"num", issue.GetNumber(),
				)
			}

			return st.print(inv.Stdout)
		},
		Options: []serpent.Option{
			{
				Flag:  "install-id",
				Value: serpent.StringOf(&installID),
			},
			{
				Flag:  "user",
				Value: serpent.StringOf(&user),
			},
			{
				Flag:  "repo",
				Value: serpent.StringOf(&repo),
			},
			{
				Flag:        "n-issues",
				Description: "Number of issues to test.",
				Value:       serpent.Int64Of(&nIssues),
				Default:     "10",
			},
		},
	}
}
