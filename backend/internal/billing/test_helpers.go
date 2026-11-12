package billing

import (
	"database/sql"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
)

var testDB *sqlx.DB

func getTestDB(t *testing.T) *sqlx.DB {
	if testDB != nil {
		return testDB
	}
	dsn := "root:root@tcp(127.0.0.1:3306)/llm_gateway?parseTime=true&charset=utf8mb4"
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Skip("MySQL not available for testing")
	}
	testDB = sqlx.NewDb(db, "mysql")
	return testDB
}

func newTestRedisClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr})
}

