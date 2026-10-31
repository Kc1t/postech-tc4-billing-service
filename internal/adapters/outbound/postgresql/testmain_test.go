package postgresql

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/migrations"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Os testes deste pacote rodam contra um Postgres real, configurado por TEST_DB_* (os mesmos
// nomes usados pelos testes do chassis): host, porta, usuário, senha e banco.
var testDB *gorm.DB

func TestMain(m *testing.M) {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		env("TEST_DB_HOST", "localhost"), env("TEST_DB_PORT", "5432"), env("TEST_DB_USER", "postgres"),
		env("TEST_DB_PASSWORD", "postgres"), env("TEST_DB_NAME", "workshop"))
	db, err := Open(dsn, PoolConfig{MaxOpenConns: 10})
	if err != nil {
		log.Fatalf("banco de teste: %v", err)
	}
	if _, err := Migrate(context.Background(), db, migrations.FS); err != nil {
		log.Fatalf("migrations: %v", err)
	}
	testDB = db
	os.Exit(m.Run())
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var now = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

// seedBudget cria (via domínio) e grava um orçamento com os_id único, removido no fim do teste.
func seedBudget(t *testing.T, approve bool) *entities.Budget {
	t.Helper()
	b, err := entities.NewBudget(entities.NewBudgetInput{
		OSID: "os-" + uuid.NewString(), OSCode: 101, SagaID: "saga-" + uuid.NewString(),
		Requester: entities.Requester{ID: "req-1", Name: "Ana", Email: "ana@example.com", Document: "52998224725"},
		Items: []entities.BudgetItem{
			{Kind: entities.ItemService, Ref: "1", Description: "Revisão", Quantity: 1, UnitPrice: 300},
			{Kind: entities.ItemPart, Ref: "OIL-5W30", Description: "Óleo", Quantity: 4, UnitPrice: 37.45},
		},
		Total: 449.8,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if approve {
		if err := b.Approve(now); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewBudgetRepository(testDB).Create(context.Background(), b); err != nil {
		t.Fatalf("seed budget: %v", err)
	}
	t.Cleanup(func() {
		testDB.Exec("DELETE FROM payments WHERE budget_id = ?", b.ID())
		testDB.Exec("DELETE FROM budgets WHERE id = ?", b.ID())
	})
	return b
}

func seedPayment(t *testing.T, b *entities.Budget) *entities.Payment {
	t.Helper()
	p, err := entities.NewPayment(b, entities.ProviderFake, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewPaymentRepository(testDB).Create(context.Background(), p); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	return p
}
