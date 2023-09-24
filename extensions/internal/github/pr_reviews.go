package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/augmentable-dev/vtab"
	"github.com/cenkalti/backoff/v4"
	"github.com/mergestat/mergestat-lite/extensions/options"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
	"go.riyazali.net/sqlite"
)

const minPerPage = 25

type pullRequestForReviews struct {
	Id      githubv4.String
	Number  int
	Reviews struct {
		Nodes    []*prReview
		PageInfo struct {
			EndCursor   githubv4.String
			HasNextPage bool
		}
	} `graphql:"reviews(first: $perPage, after: $reviewCursor)"`
}

type prReview struct {
	Author struct {
		Login string
		Url   string
	}
	AuthorAssociation         string
	AuthorCanPushToRepository bool
	Body                      string
	Comments                  struct {
		TotalCount int
	}
	CreatedAt       githubv4.DateTime
	CreatedViaEmail bool
	Editor          struct {
		Login string
	}
	Id           string
	LastEditedAt githubv4.DateTime
	PublishedAt  githubv4.DateTime
	State        string
	SubmittedAt  githubv4.DateTime
	UpdatedAt    githubv4.DateTime
}

type fetchPRReviewsResults struct {
	RateLimit   *options.GitHubRateLimitResponse
	PullRequest *pullRequestForReviews
	HasNextPage bool
	EndCursor   *githubv4.String
}

func (i *iterPRReviews) fetchPRReviews(ctx context.Context, endCursor *githubv4.String) (*fetchPRReviewsResults, error) {
	var PRQuery struct {
		RateLimit  *options.GitHubRateLimitResponse
		Repository struct {
			Owner struct {
				Login string
			}
			Name        string
			PullRequest pullRequestForReviews `graphql:"pullRequest(number: $prNumber)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	variables := map[string]interface{}{
		"owner":        githubv4.String(i.owner),
		"name":         githubv4.String(i.name),
		"prNumber":     githubv4.Int(i.prNumber),
		"perPage":      githubv4.Int(i.PerPage),
		"reviewCursor": endCursor,
	}

	err := i.Client().Query(ctx, &PRQuery, variables)
	if err != nil {
		return nil, err
	}

	return &fetchPRReviewsResults{
		RateLimit:   PRQuery.RateLimit,
		PullRequest: &PRQuery.Repository.PullRequest,
		HasNextPage: PRQuery.Repository.PullRequest.Reviews.PageInfo.HasNextPage,
		EndCursor:   &PRQuery.Repository.PullRequest.Reviews.PageInfo.EndCursor,
	}, nil
}

type iterPRReviews struct {
	*Options
	owner         string
	name          string
	prNumber      int
	currentReview int
	results       *fetchPRReviewsResults
}

func (i *iterPRReviews) logger() *zerolog.Logger {
	logger := i.Logger.With().Int("per-page", i.PerPage).Str("owner", i.owner).Str("name", i.name).Int("pr-number", i.prNumber).Logger()
	return &logger
}

func (i *iterPRReviews) Column(ctx vtab.Context, c int) error {
	current := i.results.PullRequest.Reviews.Nodes[i.currentReview]
	col := prReviewCols[c]

	switch col.Name {
	case "author_login":
		ctx.ResultText(current.Author.Login)
	case "author_url":
		ctx.ResultText(current.Author.Url)
	case "author_association":
		ctx.ResultText(current.AuthorAssociation)
	case "author_can_push_to_repository":
		ctx.ResultInt(t1f0(current.AuthorCanPushToRepository))
	case "body":
		ctx.ResultText(current.Body)
	case "comment_count":
		ctx.ResultInt(current.Comments.TotalCount)
	case "created_at":
		t := current.CreatedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "created_via_email":
		ctx.ResultInt(t1f0(current.CreatedViaEmail))
	case "editor_login":
		ctx.ResultText(current.Editor.Login)
	case "id":
		ctx.ResultText(current.Id)
	case "last_edited_at":
		t := current.LastEditedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "published_at":
		t := current.PublishedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "state":
		ctx.ResultText(current.State)
	case "submitted_at":
		t := current.SubmittedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "updated_at":
		t := current.UpdatedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "pr_number":
		ctx.ResultInt(i.results.PullRequest.Number)
	}
	return nil
}

func (i *iterPRReviews) Next() (vtab.Row, error) {
	i.logger().Info().Msgf("iterPRReviews.PerPage: %d, iterPRReviews.Options.PerPage: %d", i.PerPage, i.Options.PerPage)
	// restore original PerPage value when done
	defer func() {
		i.PerPage = i.Options.PerPage
	}()

	i.currentReview += 1

	op := func() error {
		// empty results set or iterator past last record
		if i.results == nil || i.currentReview >= len(i.results.PullRequest.Reviews.Nodes) {
			// empty results set or has more github pages to fetch from
			if i.results == nil || i.results.HasNextPage {
				if err := i.RateLimiter.Wait(context.Background()); err != nil {
					// as long as the context.Background() is used above (eg, can't be cancelled, etc.), we can safely retry on error
					i.logger().Error().Msgf("rate limiter wait failed (will retry): %v", err)
					return err
				}

				var cursor *githubv4.String
				if i.results != nil {
					cursor = i.results.EndCursor
				}

				i.Options.GitHubPreRequestHook()

				l := i.logger().With().Interface("cursor", cursor).Logger()
				l.Info().Msgf("fetching page of pr_reviews for %s/%s", i.owner, i.name)
				results, err := i.fetchPRReviews(context.Background(), cursor)

				i.Options.GitHubPostRequestHook()

				if err != nil {
					l.Error().Msgf("fetching page of pr_reviews for %s/%s failed: %v", i.owner, i.name, err)
					// retry on "502 Bad Gateway" github error with halved PerPage but not less than minPerPage
					// note: fetchPRReviews() uses i.PerPage when calling github api
					if strings.Contains(err.Error(), http.StatusText(http.StatusBadGateway)) {
						perPage := i.PerPage / 2
						if perPage < minPerPage {
							perPage = minPerPage
						}
						i.PerPage = perPage
						l.Info().Msgf("will retry fetching page of pr_reviews for %s/%s with 'github_per_page' of %d", i.owner, i.name, i.PerPage)
						return err
					}
					// don't retry for other github errors
					return &backoff.PermanentError{Err: err}
				}

				i.Options.RateLimitHandler(results.RateLimit)

				i.results = results
				i.currentReview = 0

				if len(results.PullRequest.Reviews.Nodes) == 0 {
					// don't retry if there are no new nodes
					return &backoff.PermanentError{Err: io.EOF}
				}
				// continue processing new github page
				return nil
			}
			// don't retry if all github pages are already fetched & processed
			return &backoff.PermanentError{Err: io.EOF}
		}
		// continue processing current github page
		return nil
	}
	// exponential backoff
	exp := backoff.NewExponentialBackOff()
	exp.InitialInterval = 500 * time.Millisecond
	exp.MaxInterval = time.Minute
	exp.MaxElapsedTime = 5 * time.Minute
	if err := backoff.Retry(op, exp); err != nil {
		return nil, err
	}

	return i, nil
}

var prReviewCols = []vtab.Column{
	{Name: "owner", Type: "TEXT", NotNull: true, Hidden: true, Filters: []*vtab.ColumnFilter{{Op: sqlite.INDEX_CONSTRAINT_EQ, OmitCheck: true}}},
	{Name: "reponame", Type: "TEXT", NotNull: true, Hidden: true, Filters: []*vtab.ColumnFilter{{Op: sqlite.INDEX_CONSTRAINT_EQ, OmitCheck: true}}},
	{Name: "pr_number", Type: "INT", NotNull: true, Hidden: true, Filters: []*vtab.ColumnFilter{{Op: sqlite.INDEX_CONSTRAINT_EQ, OmitCheck: true}}},
	{Name: "author_login", Type: "TEXT"},
	{Name: "author_url", Type: "TEXT"},
	{Name: "author_association", Type: "TEXT"},
	{Name: "author_can_push_to_repository", Type: "BOOLEAN"},
	{Name: "body", Type: "TEXT"},
	{Name: "comment_count", Type: "INT"},
	{Name: "created_at", Type: "DATETIME"},
	{Name: "created_via_email", Type: "BOOLEAN"},
	{Name: "editor_login", Type: "TEXT"},
	{Name: "id", Type: "TEXT"},
	{Name: "last_edited_at", Type: "DATETIME"},
	{Name: "published_at", Type: "DATETIME"},
	{Name: "state", Type: "TEXT"},
	{Name: "submitted_at", Type: "DATETIME"},
	{Name: "updated_at", Type: "DATETIME"},
}

func NewPRReviewsModule(opts *Options) sqlite.Module {
	return vtab.NewTableFunc("github_repo_pr_reviews", prReviewCols, func(constraints []*vtab.Constraint, orders []*sqlite.OrderBy) (vtab.Iterator, error) {
		var fullNameOrOwner, name, owner string
		var nameOrNumber *sqlite.Value
		var number int
		threeArgs := false // if true, user supplied 3 args, 1st is org name, 2nd is repo name, 3rd is pr number
		for _, constraint := range constraints {
			if constraint.Op == sqlite.INDEX_CONSTRAINT_EQ {
				switch constraint.ColIndex {
				case 0:
					fullNameOrOwner = constraint.Value.Text()
				case 1:
					nameOrNumber = constraint.Value
				case 2:
					if constraint.Value.Int() <= 0 {
						return nil, fmt.Errorf("please supply a pull request number")
					}
					number = constraint.Value.Int()
					threeArgs = true
				}

			}
		}
		if !threeArgs {
			if nameOrNumber == nil || nameOrNumber.Type() != sqlite.SQLITE_INTEGER {
				return nil, fmt.Errorf("please supply a valid pr number")
			}
			number = nameOrNumber.Int()
			var err error
			owner, name, err = repoOwnerAndName("", fullNameOrOwner)
			if err != nil {
				return nil, err
			}

			if number <= 0 {
				return nil, fmt.Errorf("please supply a valid pull request number")
			}
		} else {
			owner = fullNameOrOwner
			name = nameOrNumber.Text()
		}

		iter := &iterPRReviews{opts, owner, name, number, -1, nil}
		iter.logger().Info().Msgf("starting GitHub repo_pr_reviews iterator for %s/%s pr : %d", owner, name, number)
		return iter, nil
	}, vtab.EarlyOrderByConstraintExit(true))
}
