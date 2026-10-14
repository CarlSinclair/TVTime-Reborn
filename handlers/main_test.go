package handlers_test

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"tvt/auth"
)

func TestMain(m *testing.M) {
	auth.HashCost = bcrypt.MinCost
	os.Exit(m.Run())
}
