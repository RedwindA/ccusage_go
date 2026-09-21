package sourcesb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenDBEscapesPathAndPreservesReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage #1.sqlite")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec("CREATE TABLE fixture (value INTEGER); INSERT INTO fixture VALUES (42)"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err = db.QueryRow("SELECT value FROM fixture").Scan(&got); err != nil || got != 42 {
		t.Fatalf("read: value=%d error=%v", got, err)
	}
	if _, err = db.Exec("INSERT INTO fixture VALUES (99)"); err == nil {
		t.Fatal("read-only database accepted a write")
	}
}
