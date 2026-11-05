package handlers_test

import (
	"os"
	"testing"

	"github.com/CarlSinclair/csuit-libs/validate"
	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) {
	validate.BcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}
