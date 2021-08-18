package github

import (
	"context"
	"io"
	"time"

	"github.com/augmentable-dev/vtab"
	"github.com/shurcooL/githubv4"
	"go.riyazali.net/sqlite"
	"golang.org/x/time/rate"
)

type fetchUserReposOptions struct {
	Client          *githubv4.Client
	Login           string
	PerPage         int
	UserReposCursor *githubv4.String
	RepositoryOrder *githubv4.RepositoryOrder
}

type fetchUserReposResults struct {
	UserRepos   []*userRepo
	HasNextPage bool
	EndCursor   *githubv4.String
}

type userRepo struct {
	CreatedAt        time.Time
	DatabaseId       int
	DefaultBranchRef struct {
		Name   string
		Prefix string
	}
	Description string
	DiskUsage   int
	ForkCount   int
	HomepageUrl string
	IsArchived  bool
	IsDisabled  bool
	IsFork      bool
	IsMirror    bool
	IsPrivate   bool
	Issues      struct {
		TotalCount int
	}
	LatestRelease struct {
		Author struct {
			Login string
		}
		CreatedAt   githubv4.DateTime
		Name        string
		PublishedAt githubv4.DateTime
	}
	LicenseInfo struct {
		Key      string
		Name     string
		Nickname string
	}
	Name              string
	OpenGraphImageUrl githubv4.URI
	PrimaryLanguage   struct {
		Name string
	}
	PullRequests struct {
		TotalCount int
	}
	PushedAt time.Time
	Releases struct {
		TotalCount int
	}
	StargazerCount int
	UpdatedAt      time.Time
	Watchers       struct {
		TotalCount int
	}
}

func fetchUserRepos(ctx context.Context, input *fetchUserReposOptions) (*fetchUserReposResults, error) {
	var reposQuery struct {
		User struct {
			Login        string
			Repositories struct {
				Nodes    []*userRepo
				PageInfo struct {
					EndCursor   githubv4.String
					HasNextPage bool
				}
			} `graphql:"repositories(first: $perPage, after: $userReposCursor, orderBy: $repositoryOrder)"`
		} `graphql:"user(login: $login)"`
	}

	variables := map[string]interface{}{
		"login":           githubv4.String(input.Login),
		"perPage":         githubv4.Int(input.PerPage),
		"userReposCursor": (*githubv4.String)(input.UserReposCursor),
		"repositoryOrder": input.RepositoryOrder,
	}

	err := input.Client.Query(ctx, &reposQuery, variables)
	if err != nil {
		return nil, err
	}

	return &fetchUserReposResults{
		reposQuery.User.Repositories.Nodes,
		reposQuery.User.Repositories.PageInfo.HasNextPage,
		&reposQuery.User.Repositories.PageInfo.EndCursor,
	}, nil
}

type iterUserRepos struct {
	login       string
	client      *githubv4.Client
	current     int
	results     *fetchUserReposResults
	rateLimiter *rate.Limiter
	repoOrder   *githubv4.RepositoryOrder
}

func (i *iterUserRepos) Column(ctx *sqlite.Context, c int) error {
	current := i.results.UserRepos[i.current]
	switch userReposCols[c].Name {
	case "login":
		ctx.ResultText(i.login)
	case "created_at":
		t := current.CreatedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "database_id":
		ctx.ResultInt(current.DatabaseId)
	case "default_branch_ref_name":
		ctx.ResultText(current.DefaultBranchRef.Name)
	case "default_branch_ref_prefix":
		ctx.ResultText(current.DefaultBranchRef.Prefix)
	case "description":
		ctx.ResultText(current.Description)
	case "disk_usage":
		ctx.ResultInt(current.DiskUsage)
	case "fork_count":
		ctx.ResultInt(current.ForkCount)
	case "homepage_url":
		ctx.ResultText(current.HomepageUrl)
	case "is_archived":
		ctx.ResultInt(t1f0(current.IsArchived))
	case "is_disabled":
		ctx.ResultInt(t1f0(current.IsDisabled))
	case "is_fork":
		ctx.ResultInt(t1f0(current.IsFork))
	case "is_mirror":
		ctx.ResultInt(t1f0(current.IsMirror))
	case "is_private":
		ctx.ResultInt(t1f0(current.IsPrivate))
	case "issue_count":
		ctx.ResultInt(current.Issues.TotalCount)
	case "latest_release_author":
		ctx.ResultText(current.LatestRelease.Author.Login)
	case "latest_release_created_at":
		t := current.LatestRelease.CreatedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "latest_release_name":
		ctx.ResultText(current.LatestRelease.Name)
	case "latest_release_published_at":
		t := current.LatestRelease.PublishedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "license_key":
		ctx.ResultText(current.LicenseInfo.Key)
	case "license_name":
		ctx.ResultText(current.LicenseInfo.Name)
	case "name":
		ctx.ResultText(current.Name)
	case "open_graph_image_url":
		ctx.ResultText(current.OpenGraphImageUrl.String())
	case "primary_language":
		ctx.ResultText(current.PrimaryLanguage.Name)
	case "pull_request_count":
		ctx.ResultInt(current.PullRequests.TotalCount)
	case "pushed_at":
		t := current.PushedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "release_count":
		ctx.ResultInt(current.Releases.TotalCount)
	case "stargazer_count":
		ctx.ResultInt(current.StargazerCount)
	case "updated_at":
		t := current.UpdatedAt
		if t.IsZero() {
			ctx.ResultNull()
		} else {
			ctx.ResultText(t.Format(time.RFC3339Nano))
		}
	case "watcher_count":
		ctx.ResultInt(current.Watchers.TotalCount)
	}
	return nil
}

func (i *iterUserRepos) Next() (vtab.Row, error) {
	i.current += 1

	if i.results == nil || i.current >= len(i.results.UserRepos) {
		if i.results == nil || i.results.HasNextPage {
			err := i.rateLimiter.Wait(context.Background())
			if err != nil {
				return nil, err
			}

			var cursor *githubv4.String
			if i.results != nil {
				cursor = i.results.EndCursor
			}
			results, err := fetchUserRepos(context.Background(), &fetchUserReposOptions{i.client, i.login, 100, cursor, i.repoOrder})
			if err != nil {
				return nil, err
			}

			i.results = results
			i.current = 0

		} else {
			return nil, io.EOF
		}
	}

	return i, nil
}

var userReposCols = []vtab.Column{
	{Name: "login", Type: sqlite.SQLITE_TEXT, Hidden: true, Filters: []*vtab.ColumnFilter{{Op: sqlite.INDEX_CONSTRAINT_EQ, Required: true, OmitCheck: true}}},
	{Name: "created_at", Type: sqlite.SQLITE_TEXT, OrderBy: vtab.ASC | vtab.DESC},
	{Name: "database_id", Type: sqlite.SQLITE_INTEGER},
	{Name: "default_branch_ref_name", Type: sqlite.SQLITE_TEXT},
	{Name: "default_branch_ref_prefix", Type: sqlite.SQLITE_TEXT},
	{Name: "description", Type: sqlite.SQLITE_TEXT},
	{Name: "disk_usage", Type: sqlite.SQLITE_INTEGER},
	{Name: "fork_count", Type: sqlite.SQLITE_INTEGER},
	{Name: "homepage_url", Type: sqlite.SQLITE_TEXT},
	{Name: "is_archived", Type: sqlite.SQLITE_INTEGER},
	{Name: "is_disabled", Type: sqlite.SQLITE_INTEGER},
	{Name: "is_fork", Type: sqlite.SQLITE_INTEGER},
	{Name: "is_mirror", Type: sqlite.SQLITE_INTEGER},
	{Name: "is_private", Type: sqlite.SQLITE_INTEGER},
	{Name: "issue_count", Type: sqlite.SQLITE_INTEGER},
	{Name: "latest_release_author", Type: sqlite.SQLITE_TEXT},
	{Name: "latest_release_created_at", Type: sqlite.SQLITE_TEXT},
	{Name: "latest_release_name", Type: sqlite.SQLITE_TEXT},
	{Name: "latest_release_published_at", Type: sqlite.SQLITE_TEXT},
	{Name: "license_key", Type: sqlite.SQLITE_TEXT},
	{Name: "license_name", Type: sqlite.SQLITE_TEXT},
	{Name: "name", Type: sqlite.SQLITE_TEXT, OrderBy: vtab.ASC | vtab.DESC},
	{Name: "open_graph_image_url", Type: sqlite.SQLITE_TEXT},
	{Name: "primary_language", Type: sqlite.SQLITE_TEXT},
	{Name: "pull_request_count", Type: sqlite.SQLITE_INTEGER},
	{Name: "pushed_at", Type: sqlite.SQLITE_TEXT, OrderBy: vtab.ASC | vtab.DESC},
	{Name: "release_count", Type: sqlite.SQLITE_INTEGER},
	{Name: "stargazer_count", Type: sqlite.SQLITE_INTEGER, OrderBy: vtab.ASC | vtab.DESC},
	{Name: "updated_at", Type: sqlite.SQLITE_TEXT, OrderBy: vtab.ASC | vtab.DESC},
	{Name: "watcher_count", Type: sqlite.SQLITE_INTEGER},
}

func NewUserReposModule(opts *Options) sqlite.Module {
	return vtab.NewTableFunc("github_user_repos", userReposCols, func(constraints []*vtab.Constraint, orders []*sqlite.OrderBy) (vtab.Iterator, error) {
		var login string
		for _, constraint := range constraints {
			if constraint.Op == sqlite.INDEX_CONSTRAINT_EQ {
				switch constraint.ColIndex {
				case 0:
					login = constraint.Value.Text()
				}
			}
		}

		var repoOrder *githubv4.RepositoryOrder
		// for now we can only support single field order bys
		if len(orders) == 1 {
			repoOrder = &githubv4.RepositoryOrder{}
			order := orders[0]
			switch userReposCols[order.ColumnIndex].Name {
			case "name":
				repoOrder.Field = githubv4.RepositoryOrderFieldName
			case "created_at":
				repoOrder.Field = githubv4.RepositoryOrderFieldCreatedAt
			case "updated_at":
				repoOrder.Field = githubv4.RepositoryOrderFieldUpdatedAt
			case "pushed_at":
				repoOrder.Field = githubv4.RepositoryOrderFieldPushedAt
			case "stargazer_count":
				repoOrder.Field = githubv4.RepositoryOrderFieldStargazers
			}
			repoOrder.Direction = orderByToGitHubOrder(order.Desc)
		}

		return &iterUserRepos{login, opts.Client(), -1, nil, opts.RateLimiter, repoOrder}, nil
	})
}
