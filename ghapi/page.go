package ghapi

import (
	"context"
	"fmt"

	"github.com/google/go-github/v59/github"
)

// Page returns at most n items from a paginated list.
func Page[T any](
	ctx context.Context,
	client *github.Client,
	get func(context.Context, *github.ListOptions) ([]T, *github.Response, error),
	n int,
) ([]T, error) {
	var all []T
	if n == 0 {
		return all, nil
	}
	if n < 0 {
		return nil, fmt.Errorf("n must be non-negative")
	}
	opt := &github.ListOptions{PerPage: 100}
	for {
		items, resp, err := get(ctx, opt)
		if err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		for _, item := range items {
			all = append(all, item)
			if len(all) == n {
				return all, nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return all, nil
}

func OnlyTrueIssues(
	slice []*github.Issue,
) []*github.Issue {
	var result []*github.Issue
	for _, item := range slice {
		if item.IsPullRequest() {
			continue
		}
		result = append(result, item)
	}
	return result
}
