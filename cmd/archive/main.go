package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/google/go-github/v61/github"
	_ "github.com/jackc/pgx/stdlib"
	"github.com/joho/godotenv"
)

var ctxBG = context.Background()

var pool *sql.DB

type data struct {
	ID          int64
	FullName    string
	Description string
	HTMLURL     string
	Homepage    string
	Topics      []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type repo struct {
	Name string
	Data data
}

type jsonRepo struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	HTMLURL     string `json:"htmlurl"`
	Homepage    string `json:"homepage"`
	Topics      string `json:"topics"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	UID         int    `json:"uid"`
}

// TODO: store repos in pg
// TODO: send email to pm
// TODO: serve repos from api

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("failed to load .env", err)
	}

	ghPAT := os.Getenv("GH_PAT")
	gc := github.NewClient(nil).WithAuthToken(ghPAT)

	permalikRepos := ghRepos(gc, "permalik", false)
	var allRepos []repo
	if len(permalikRepos) > 0 {
		allRepos = append(allRepos, permalikRepos...)
	}

	dsn := os.Getenv("DSN")
	pool, err = sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal("unable to use dsn", err)
	}
	defer pool.Close()

	pool.SetConnMaxLifetime(0)
	pool.SetMaxIdleConns(3)
	pool.SetMaxOpenConns(3)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	appSignal := make(chan os.Signal, 3)
	signal.Notify(appSignal, os.Interrupt)

	go func() {
		<-appSignal
		stop()
	}()

	ping(ctx)

	dropRepos(ctx)
	createRepos(ctx)
	for _, v := range allRepos {
		insertRepos(ctx, v)
	}
	selectRepos(ctx)
}

func ping(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	if err := pool.PingContext(ctx); err != nil {
		log.Fatalf("unable to connect to database:\n%v", err)
	}
}

func createRepos(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	createQuery := `CREATE TABLE repos (
        id SERIAL PRIMARY KEY,
        owner VARCHAR(100),
        name VARCHAR(100),
        category VARCHAR(100),
        description VARCHAR(200),
        html_url VARCHAR(100),
        homepage VARCHAR(100),
        topics TEXT,
        created_at VARCHAR(10),
        updated_at VARCHAR(10),
        uid INT
    )`

	_, err := pool.ExecContext(ctx, createQuery)
	if err != nil {
		log.Fatal("unable to create table", err)
	}
}

func dropRepos(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := pool.ExecContext(ctx, "DROP TABLE repos;")
	if err != nil {
		log.Fatal("unable to drop table", err)
	}
}

func insertRepos(ctx context.Context, r repo) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ownerBefore, nameAfter, _ := strings.Cut(r.Data.FullName, "/")
	owner := ownerBefore
	name := nameAfter

	categoryBefore, descriptionAfter, _ := strings.Cut(r.Data.Description, ":")
	category := categoryBefore
	description := descriptionAfter

	var topics string
	for _, v := range r.Data.Topics {
		if len(topics) < 1 {
			topics = v
		} else {
			topics = fmt.Sprintf("%s,%s", topics, v)
		}
	}

	createdAt := r.Data.CreatedAt.Format("2006-01-02")
	updatedAt := r.Data.UpdatedAt.Format("2006-01-02")

	query := `
    INSERT INTO repos (
        owner,
        name,
        category,
        description,
        html_url,
        homepage,
        topics,
        created_at,
        updated_at,
        uid
    )
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    RETURNING id;
    `

	result, err := pool.ExecContext(ctx, query,
		owner,
		name,
		category,
		description,
		r.Data.HTMLURL,
		r.Data.Homepage,
		topics,
		createdAt,
		updatedAt,
		r.Data.ID)
	if err != nil {
		log.Fatal("failed executing insert", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		log.Fatal("failed writing to db", err)
	}
	if rows != 1 {
		log.Fatalf("expected to affect 1 row, affected %d rows", rows)
	}
}

func selectRepos(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := pool.QueryContext(ctx, "select * from repos;")
	if err != nil {
		log.Fatal("unable to execute select all", err)
	}
	defer rows.Close()

	var repos []jsonRepo
	for rows.Next() {
		var (
			id          int
			owner       string
			name        string
			category    string
			description string
			htmlURL     string
			homepage    string
			topics      string
			createdAt   string
			updatedAt   string
			uid         int
		)
		if err := rows.Scan(
			&id,
			&owner,
			&name,
			&category,
			&description,
			&htmlURL,
			&homepage,
			&topics,
			&createdAt,
			&updatedAt,
			&uid); err != nil {
			log.Fatal(err)
		}
		repo := jsonRepo{
			Owner:       owner,
			Name:        name,
			Category:    category,
			Description: description,
			HTMLURL:     htmlURL,
			Homepage:    homepage,
			Topics:      topics,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			UID:         uid,
		}
		repos = append(repos, repo)
	}

	rerr := rows.Close()
	if rerr != nil {
		log.Fatal(rerr)
	}

	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	jsonData, err := json.Marshal(repos)
	if err != nil {
		log.Fatalf("error marshaling to json:\n%v", err)
	}
	fmt.Println(string(jsonData))
	return jsonData, nil
}

func parseGH(repo repo, arr []repo, ghData []*github.Repository) []repo {
	for _, v := range ghData {
		timestampCA := v.GetCreatedAt()
		pointerCA := timestampCA.GetTime()
		createdAt := *pointerCA
		timestampUA := v.GetUpdatedAt()
		pointerUA := timestampUA.GetTime()
		updatedAt := *pointerUA
		d := data{
			ID:          v.GetID(),
			FullName:    v.GetFullName(),
			Description: v.GetDescription(),
			HTMLURL:     v.GetHTMLURL(),
			Homepage:    v.GetHomepage(),
			Topics:      v.Topics,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		}
		repo.Name = v.GetName()
		repo.Data = d
		arr = append(arr, repo)
	}
	return arr
}

func ghRepos(gc *github.Client, name string, isOrg bool) []repo {
	var r repo
	var arr []repo
	listOpt := github.ListOptions{Page: 1, PerPage: 25}

	if isOrg {
		opts := &github.RepositoryListByOrgOptions{Type: "public", Sort: "created", ListOptions: listOpt}
		data, _, err := gc.Repositories.ListByOrg(ctxBG, name, opts)
		if err != nil {
			log.Fatalf("github: ListByOrg\n%v", err)
		}
		if len(data) <= 0 {
			log.Fatalf("github: no data returned from GithubAll")
		}
		arr = parseGH(r, arr, data)
		return arr
	} else {
		opts := &github.RepositoryListByUserOptions{Type: "public", Sort: "created", ListOptions: listOpt}
		data, _, err := gc.Repositories.ListByUser(ctxBG, name, opts)
		if err != nil {
			log.Fatalf("github: ListByUser\n%v", err)
		}
		if len(data) <= 0 {
			log.Fatalf("github: no data returned from GithubAll\n%s", name)
		}
		arr = parseGH(r, arr, data)
		return arr
	}
}
