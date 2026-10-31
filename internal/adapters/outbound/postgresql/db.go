// Package postgresql é o adapter de persistência do billing-service (PostgreSQL via GORM):
// repositórios dos agregados, unidade de trabalho com outbox, inbox e migrations versionadas.
package postgresql

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/newrelic/go-agent/v3/integrations/nrpgx5"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// PoolConfig ajusta o pool de conexões.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Open conecta ao Postgres. O log do GORM fica em Warn, sem cor e com consultas parametrizadas:
// consultas lentas e erros aparecem, mas sem os valores (CPF, e-mail) — e "record not found",
// que é fluxo normal (404, idempotência), não vira ruído.
//
// A conexão é pelo pgx com o tracer do New Relic: cada consulta feita com um contexto que carrega
// transação vira um segmento de datastore, e o banco aparece no trace e no service map.
func Open(dsn string, pool PoolConfig) (*gorm.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgresql: dsn inválida: %w", err)
	}
	cfg.Tracer = nrpgx5.NewTracer()
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: stdlib.OpenDB(*cfg)}), &gorm.Config{
		Logger: gormlogger.New(log.New(os.Stderr, "", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
		SkipDefaultTransaction: true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("postgresql: conectar: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if pool.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	}
	if pool.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	}
	if pool.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)
	}
	return db, nil
}

// Ping verifica a conexão (usado no readiness).
func Ping(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// migrationLockID identifica o advisory lock das migrations ("billing" em hex). Com várias
// réplicas subindo juntas, só uma aplica as migrations; as outras esperam e encontram tudo pronto.
const migrationLockID = 0x62696c6c696e67

// Migrate aplica, em ordem e numa única transação, as migrations *.sql de fsys ainda não
// registradas em schema_migrations. DDL no Postgres é transacional: ou todas as pendentes
// entram, ou nenhuma. Devolve as versões aplicadas nesta execução.
func Migrate(ctx context.Context, db *gorm.DB, fsys fs.FS) ([]string, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var applied []string
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationLockID).Error; err != nil {
			return fmt.Errorf("travar migrations: %w", err)
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text        PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`).Error; err != nil {
			return fmt.Errorf("criar schema_migrations: %w", err)
		}
		var done []string
		if err := tx.Raw("SELECT version FROM schema_migrations").Scan(&done).Error; err != nil {
			return err
		}
		seen := make(map[string]bool, len(done))
		for _, v := range done {
			seen[v] = true
		}
		for _, name := range files {
			version := strings.TrimSuffix(name, ".sql")
			if seen[version] {
				continue
			}
			sql, err := fs.ReadFile(fsys, name)
			if err != nil {
				return err
			}
			if err := tx.Exec(string(sql)).Error; err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version).Error; err != nil {
				return err
			}
			applied = append(applied, version)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("postgresql: %w", err)
	}
	return applied, nil
}

// isUniqueViolation reconhece violação de UNIQUE (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
