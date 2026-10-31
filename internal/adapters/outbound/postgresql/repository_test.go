package postgresql

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/migrations"
	"github.com/google/uuid"
)

func TestMigrate_Idempotente(t *testing.T) {
	applied, err := Migrate(context.Background(), testDB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("segunda execução não deveria aplicar nada, aplicou %v", applied)
	}
	var versions []string
	testDB.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions)
	if len(versions) < 3 || versions[0] != "0001_create_budgets" {
		t.Fatalf("versões registradas: %v", versions)
	}
}

func TestMigrate_FalhaDesfazTudo(t *testing.T) {
	bad := fstest.MapFS{
		"9998_ok.sql":       {Data: []byte("CREATE TABLE zz_migration_probe (id int);")},
		"9999_quebra.sql":   {Data: []byte("CREATE TABLE sintaxe errada (;")},
		"ignorado.txt":      {Data: []byte("não é sql")},
		"0001_create_x.sql": {Data: []byte("SELECT 1;")}, // já aplicada pelo nome? não: versão diferente
	}
	t.Cleanup(func() {
		testDB.Exec("DROP TABLE IF EXISTS zz_migration_probe")
		testDB.Exec("DELETE FROM schema_migrations WHERE version IN ('0001_create_x','9998_ok','9999_quebra')")
	})
	if _, err := Migrate(context.Background(), testDB, bad); err == nil {
		t.Fatal("esperava erro na migration quebrada")
	}
	var n int64
	testDB.Raw("SELECT count(*) FROM information_schema.tables WHERE table_name = 'zz_migration_probe'").Scan(&n)
	if n != 0 {
		t.Fatal("a migration anterior à quebrada deveria ter sido desfeita")
	}
}

func TestBudgetRepository_CRUD(t *testing.T) {
	ctx := context.Background()
	repo := NewBudgetRepository(testDB)
	b := seedBudget(t, false)

	got, err := repo.FindByID(ctx, b.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.OSID() != b.OSID() || got.Total() != 449.8 || len(got.Items()) != 2 || got.Items()[1].UnitPrice != 37.45 ||
		got.Requester().Document != "52998224725" || got.Status() != entities.BudgetPendingApproval || got.Version() != 1 {
		t.Fatalf("round-trip perdeu dados: %+v", got.Snapshot())
	}
	if byOS, err := repo.FindByOSID(ctx, b.OSID()); err != nil || byOS.ID() != b.ID() {
		t.Fatalf("FindByOSID: %v", err)
	}

	if err := got.Approve(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got.Version() != 2 {
		t.Fatalf("version deveria avançar para 2, está %d", got.Version())
	}
	reloaded, _ := repo.FindByID(ctx, b.ID())
	if reloaded.Status() != entities.BudgetApproved || reloaded.DecidedAt() == nil || reloaded.Version() != 2 {
		t.Fatalf("update não persistiu: %+v", reloaded.Snapshot())
	}
}

func TestBudgetRepository_OptimisticLock(t *testing.T) {
	ctx := context.Background()
	repo := NewBudgetRepository(testDB)
	b := seedBudget(t, false)

	first, _ := repo.FindByID(ctx, b.ID())
	second, _ := repo.FindByID(ctx, b.ID())
	_ = first.Approve(now)
	if err := repo.Update(ctx, first); err != nil {
		t.Fatal(err)
	}
	_ = second.Reject("mudei de ideia", now)
	if err := repo.Update(ctx, second); !errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("escrita concorrente deveria falhar com ErrConcurrentUpdate, veio %v", err)
	}
	final, _ := repo.FindByID(ctx, b.ID())
	if final.Status() != entities.BudgetApproved {
		t.Fatalf("a primeira escrita deveria prevalecer, status %s", final.Status())
	}
}

func TestBudgetRepository_Erros(t *testing.T) {
	ctx := context.Background()
	repo := NewBudgetRepository(testDB)
	b := seedBudget(t, false)

	dup, _ := entities.NewBudget(entities.NewBudgetInput{
		OSID: b.OSID(), OSCode: 1, SagaID: "s", Requester: entities.Requester{Document: "52998224725"},
		Items: []entities.BudgetItem{{Kind: entities.ItemService, Ref: "1", Quantity: 1, UnitPrice: 1}}, Total: 1,
	}, now)
	if err := repo.Create(ctx, dup); !errors.Is(err, domainerrors.ErrBudgetAlreadyExists) {
		t.Fatalf("os_id duplicado: veio %v", err)
	}
	for _, id := range []string{"nao-e-uuid", uuid.NewString()} {
		if _, err := repo.FindByID(ctx, id); !errors.Is(err, domainerrors.ErrBudgetNotFound) {
			t.Fatalf("FindByID(%q): veio %v", id, err)
		}
	}
	if _, err := repo.FindByOSID(ctx, "os-inexistente"); !errors.Is(err, domainerrors.ErrBudgetNotFound) {
		t.Fatalf("FindByOSID: veio %v", err)
	}
	ghost := entities.RestoreBudget(entities.BudgetSnapshot{ID: uuid.NewString(), Status: entities.BudgetApproved, Version: 1})
	if err := repo.Update(ctx, ghost); !errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("update de inexistente: veio %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.FindByID(cancelled, b.ID()); err == nil {
		t.Fatal("contexto cancelado deveria falhar")
	}
	if err := repo.Create(cancelled, dup); err == nil || errors.Is(err, domainerrors.ErrBudgetAlreadyExists) {
		t.Fatalf("contexto cancelado: veio %v", err)
	}
	if err := repo.Update(cancelled, b); err == nil || errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("contexto cancelado: veio %v", err)
	}
}

func TestPaymentRepository_CRUD(t *testing.T) {
	ctx := context.Background()
	repo := NewPaymentRepository(testDB)
	b := seedBudget(t, true)
	p := seedPayment(t, b)

	got, err := repo.FindByID(ctx, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount() != 449.8 || got.Status() != entities.PaymentCreated || got.ProviderPaymentID() != "" || got.CheckoutURL() != "" {
		t.Fatalf("round-trip: %+v", got.Snapshot())
	}

	if err := got.LinkCreated("pref-1", "http://localhost:8082/fake-checkout/"+p.ID(), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := got.Approve("fake-"+p.ID(), got.Amount(), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	latest, err := repo.FindLatestByBudgetID(ctx, b.ID())
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID() != p.ID() || latest.Status() != entities.PaymentApproved || latest.Version() != 3 ||
		latest.ProviderPreferenceID() != "pref-1" || latest.ProviderPaymentID() != "fake-"+p.ID() {
		t.Fatalf("update não persistiu: %+v", latest.Snapshot())
	}
	if err := latest.Refund(now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, latest); err != nil {
		t.Fatal(err)
	}
	refunded, _ := repo.FindByID(ctx, p.ID())
	if refunded.RefundedAt() == nil || refunded.Status() != entities.PaymentRefunded {
		t.Fatalf("estorno não persistiu: %+v", refunded.Snapshot())
	}
}

func TestPaymentRepository_UmaCobrancaVivaPorOrcamento(t *testing.T) {
	ctx := context.Background()
	repo := NewPaymentRepository(testDB)
	b := seedBudget(t, true)
	first := seedPayment(t, b)

	second, _ := entities.NewPayment(b, entities.ProviderFake, now)
	if err := repo.Create(ctx, second); !errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("segunda cobrança viva deveria ser barrada, veio %v", err)
	}

	// Depois que a primeira falha, uma nova tentativa é permitida e passa a ser a mais recente.
	_ = first.Fail("recusado", "", now)
	if err := repo.Update(ctx, first); err != nil {
		t.Fatal(err)
	}
	third, _ := entities.NewPayment(b, entities.ProviderFake, now.Add(time.Second))
	if err := repo.Create(ctx, third); err != nil {
		t.Fatal(err)
	}
	latest, _ := repo.FindLatestByBudgetID(ctx, b.ID())
	if latest.ID() != third.ID() {
		t.Fatalf("mais recente deveria ser %s, veio %s", third.ID(), latest.ID())
	}
}

func TestPaymentRepository_Erros(t *testing.T) {
	ctx := context.Background()
	repo := NewPaymentRepository(testDB)
	b := seedBudget(t, true)
	p := seedPayment(t, b)

	for _, id := range []string{"x", uuid.NewString()} {
		if _, err := repo.FindByID(ctx, id); !errors.Is(err, domainerrors.ErrPaymentNotFound) {
			t.Fatalf("FindByID(%q): %v", id, err)
		}
		if _, err := repo.FindLatestByBudgetID(ctx, id); !errors.Is(err, domainerrors.ErrPaymentNotFound) {
			t.Fatalf("FindLatestByBudgetID(%q): %v", id, err)
		}
	}

	stale, _ := repo.FindByID(ctx, p.ID())
	fresh, _ := repo.FindByID(ctx, p.ID())
	_ = fresh.Cancel("x", now)
	if err := repo.Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	_ = stale.LinkCreated("p", "http://x", now)
	if err := repo.Update(ctx, stale); !errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("optimistic lock: %v", err)
	}

	// provider_payment_id é único quando presente.
	other := seedBudget(t, true)
	q := seedPayment(t, other)
	_ = q.LinkCreated("p", "http://x", now)
	q.AttachProviderPayment("mp-dup-"+p.ID(), now)
	if err := repo.Update(ctx, q); err != nil {
		t.Fatal(err)
	}
	r := seedPayment(t, seedBudget(t, true))
	_ = r.LinkCreated("p", "http://x", now)
	r.AttachProviderPayment("mp-dup-"+p.ID(), now)
	if err := repo.Update(ctx, r); !errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("provider_payment_id duplicado: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.FindByID(cancelled, p.ID()); err == nil {
		t.Fatal("contexto cancelado deveria falhar")
	}
	if err := repo.Create(cancelled, p); err == nil || errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("contexto cancelado: %v", err)
	}
	if err := repo.Update(cancelled, p); err == nil || errors.Is(err, domainerrors.ErrConcurrentUpdate) {
		t.Fatalf("contexto cancelado: %v", err)
	}
}

func TestPing(t *testing.T) {
	if err := Ping(context.Background(), testDB); err != nil {
		t.Fatal(err)
	}
	if _, err := Open("postgres://%zz", PoolConfig{}); err == nil {
		t.Error("DSN inválida deveria falhar")
	}
	if _, err := Open("host=localhost port=1 user=x dbname=x sslmode=disable connect_timeout=1", PoolConfig{}); err == nil {
		t.Fatal("esperava erro ao conectar numa porta fechada")
	}
}
