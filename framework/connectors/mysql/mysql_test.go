package mysql_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/connectors/mysql"
	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Describe says where a URL points and in which TLS mode, never who
// connects: the graph, the Studio and the logs read it.
func TestDescribeSaysWhereNeverWho(t *testing.T) {
	const sentinel = "s3ntinel-my-2d9e"
	for name, c := range map[string]struct {
		url  string
		want kit.DatabaseURL
	}{
		"url": {
			"mysql://owner:" + sentinel + "@db.internal:3307/vigie?tls=true",
			kit.DatabaseURL{Driver: "go-sql-driver/mysql", Address: "db.internal:3307", Database: "vigie", TLS: "true", Networked: true},
		},
		"mariadb, default port": {
			"mariadb://owner:" + sentinel + "@db.internal/vigie?tls=skip-verify",
			kit.DatabaseURL{Driver: "go-sql-driver/mysql", Address: "db.internal:3306", Database: "vigie", TLS: "skip-verify", Networked: true},
		},
		"no tls written": {
			"mysql://owner:" + sentinel + "@db.internal/vigie",
			kit.DatabaseURL{Driver: "go-sql-driver/mysql", Address: "db.internal:3306", Database: "vigie", Networked: true},
		},
		"tls off": {
			"mysql://owner:" + sentinel + "@10.0.0.7:3306/vigie?tls=false",
			kit.DatabaseURL{Driver: "go-sql-driver/mysql", Address: "10.0.0.7:3306", Database: "vigie", TLS: "false", Plaintext: true, Networked: true},
		},
		"the driver's DSN": {
			"owner:" + sentinel + "@tcp(db.internal:3308)/vigie?tls=preferred&parseTime=true",
			kit.DatabaseURL{Driver: "go-sql-driver/mysql", Address: "db.internal:3308", Database: "vigie", TLS: "preferred", Networked: true},
		},
		"socket": {
			"owner@unix(/var/run/mysqld/mysqld.sock)/vigie",
			kit.DatabaseURL{Driver: "go-sql-driver/mysql", Address: "/var/run/mysqld/mysqld.sock", Database: "vigie"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := mysql.Engine().Describe(secret.FromString(c.url))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("described as %+v, want %+v", got, c.want)
			}
		})
	}
	for _, bad := range []string{"postgres://owner:" + sentinel + "@db.internal/vigie", "mysql://owner:" + sentinel + "@/vigie", "owner:" + sentinel + "@tcp(db.internal:3306)vigie"} {
		_, err := mysql.Engine().Describe(secret.FromString(bad))
		if err == nil || strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "owner") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestTheEngineSpeaksMySQL(t *testing.T) {
	if mysql.Engine().Dialect() != sql.DialectMySQL {
		t.Fatal(mysql.Engine().Dialect())
	}
}
