package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/fly-apps/postgres-standalone/pkg/flypg"
	"github.com/fly-apps/postgres-standalone/pkg/flypg/admin"
	"github.com/fly-apps/postgres-standalone/pkg/supervisor"
	"github.com/pkg/errors"
)

func main() {
	if _, err := os.Stat("/data/postgres"); os.IsNotExist(err) {
		setDirOwnership()
		initializePostgres()
	}

	// TODO - This is just for dev'ing and will need to change.  Additional users will be added by users
	// and this will break things.
	if err := setDefaultHBA(); err != nil {
		PanicHandler(err)
	}

	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		fmt.Println("Configuring users")

		for range t.C {
			if err := createRequiredUsers(); err != nil {
				fmt.Println("Failed to create require users... retrying...")
				continue
			}

			fmt.Println("Successfully created users")

			return
		}
	}()

	svisor := supervisor.New("flypg", 5*time.Minute)
	svisor.AddProcess("flypg", "gosu postgres postgres -D /data/postgres/")

	svisor.StopOnSignal(syscall.SIGINT, syscall.SIGTERM)
	svisor.StartHttpListener()

	if err := svisor.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

}

func initializePostgres() {
	fmt.Println("Initializing Postgres")

	if err := ioutil.WriteFile("/data/.default_password", []byte(os.Getenv("OPERATOR_PASSWORD")), 0644); err != nil {
		PanicHandler(err)
	}

	cmd := exec.Command("gosu", "postgres", "initdb", "--pgdata", "/data/postgres", "--pwfile=/data/.default_password")
	_, err := cmd.CombinedOutput()
	if err != nil {
		PanicHandler(err)
	}

}

func setDirOwnership() {
	pgUser, err := user.Lookup("postgres")
	if err != nil {
		PanicHandler(err)
	}
	pgUID, err := strconv.Atoi(pgUser.Uid)
	if err != nil {
		PanicHandler(err)
	}
	pgGID, err := strconv.Atoi(pgUser.Gid)
	if err != nil {
		PanicHandler(err)
	}
	if err := os.Chown("/data", pgUID, pgGID); err != nil {
		PanicHandler(err)
	}
}

type HBAEntry struct {
	Type     string
	Database string
	User     string
	Address  string
	Method   string
}

func setDefaultHBA() error {
	var entries []HBAEntry

	entries = append(entries, HBAEntry{
		Type:     "local",
		Database: "all",
		User:     "postgres",
		Method:   "trust",
	})

	entries = append(entries, HBAEntry{
		Type:     "host",
		Database: "all",
		User:     "postgres",
		Address:  "0.0.0.0/0",
		Method:   "md5",
	})

	entries = append(entries, HBAEntry{
		Type:     "host",
		Database: "all",
		User:     "postgres",
		Address:  "::0/0",
		Method:   "md5",
	})

	entries = append(entries, HBAEntry{
		Type:     "host",
		Database: "all",
		User:     "all",
		Address:  "0.0.0.0/0",
		Method:   "md5",
	})

	entries = append(entries, HBAEntry{
		Type:     "host",
		Database: "all",
		User:     "all",
		Address:  "::0/0",
		Method:   "md5",
	})

	file, err := os.OpenFile("/data/postgres/pg_hba.conf", os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		str := fmt.Sprintf("%s %s %s %s %s\n", entry.Type, entry.Database, entry.User, entry.Address, entry.Method)
		_, err := file.Write([]byte(str))
		if err != nil {
			return err
		}
	}

	return nil
}

func createRequiredUsers() error {
	fmt.Println("Creating required users")

	node, err := flypg.NewNode()
	if err != nil {
		return err
	}

	conn, err := node.NewLocalConnection(context.TODO())
	if err != nil {
		return err
	}

	fmt.Printf("Connection: %+v\n", conn)

	curUsers, err := admin.ListUsers(context.TODO(), conn)
	if err != nil {
		return errors.Wrap(err, "failed to list current users")
	}

	credMap := map[string]string{
		"flypgadmin": os.Getenv("SU_PASSWORD"),
	}

	for user, pass := range credMap {
		exists := false
		for _, curUser := range curUsers {
			if user == curUser.Username {
				exists = true
			}
		}
		var sql string

		if exists {
			sql = fmt.Sprintf("ALTER USER %s WITH PASSWORD '%s'", user, pass)
		} else {
			// create user
			switch user {
			case "flypgadmin":
				fmt.Println("Creating flypgadmin")
				sql = fmt.Sprintf(`CREATE USER %s WITH SUPERUSER LOGIN PASSWORD '%s'`, user, pass)
			}

			_, err := conn.Exec(context.Background(), sql)
			if err != nil {
				return err
			}
		}
	}
	return nil

}

func PanicHandler(err error) {
	debug := os.Getenv("DEBUG")
	if debug != "" {
		fmt.Println(err.Error())
		fmt.Println("Entering debug mode... (Timeout: 10 minutes")
		time.Sleep(time.Minute * 10)
	}
	panic(err)
}
