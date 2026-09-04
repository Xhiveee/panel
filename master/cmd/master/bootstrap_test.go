package main

import (
	"path/filepath"
	"testing"

	"panel/master/internal/auth"
	"panel/master/internal/db"
)

// Reinstall over a kept data dir must reset the password, not fail.
func TestBootstrapAdminResetsExistingPassword(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	hash, _ := auth.HashPassword("oldpassword")
	if _, err := database.CreateUser("admin", hash, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapAdmin(database, "admin:newpassword123"); err != nil {
		t.Fatalf("bootstrap over existing user: %v", err)
	}
	u, err := database.UserByName("admin")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(u.PasswordHash, "newpassword123") {
		t.Fatal("password was not reset to the new value")
	}
}
