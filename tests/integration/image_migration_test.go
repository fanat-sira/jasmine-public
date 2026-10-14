package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgreSQL_ImageMigrationRoundTrip(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	dir := findMigrationsDir()
	checkpointDown, err := os.ReadFile(filepath.Join(dir, "000025_jasmine_alpha_checkpoint.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(checkpointDown)); err != nil {
		t.Fatalf("checkpoint down migration: %v", err)
	}
	down, err := os.ReadFile(filepath.Join(dir, "000022_image_ingestion.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("image down migration: %v", err)
	}
	var exists bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='meal_entries' AND column_name='meal_group_id')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("meal_group_id survived down migration")
	}
	up, err := os.ReadFile(filepath.Join(dir, "000022_image_ingestion.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("image reapply migration: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='meal_entries' AND column_name='meal_group_id')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("meal_group_id missing after reapply")
	}
	checkpointUp, err := os.ReadFile(filepath.Join(dir, "000025_jasmine_alpha_checkpoint.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(checkpointUp)); err != nil {
		t.Fatalf("checkpoint reapply migration: %v", err)
	}
}
