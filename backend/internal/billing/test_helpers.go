package billing

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"

	"github.com/lrs/llm-api-gateway/internal/store"
)

var testDB *sqlx.DB

func getTestDB(t *testing.T) *sqlx.DB {
	if testDB != nil {
		return testDB
	}
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:root@tcp(127.0.0.1:3306)/llm_gateway?parseTime=true&charset=utf8mb4"
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		skipOrFail(t, "MySQL not available for testing: %v", err)
	}
	if err := db.Ping(); err != nil {
		skipOrFail(t, "MySQL ping failed: %v", err)
	}
	testDB = sqlx.NewDb(db, "mysql")
	if err := store.Migrate(testDB); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return testDB
}

func skipOrFail(t *testing.T, format string, args ...interface{}) {
	if os.Getenv("REQUIRE_TEST_DB") == "true" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func newTestRedisClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr})
}
