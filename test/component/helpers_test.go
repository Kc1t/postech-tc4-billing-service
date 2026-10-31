//go:build component

// Package component sobe o billing-service inteiro em processo, com dependências reais —
// PostgreSQL e SNS/SQS no LocalStack — e verifica o contrato com a saga: comando na fila
// billing-commands → efeito no banco/HTTP → evento no tópico billing-events.
//
// Cada execução cria um banco, uma fila de comandos, um tópico de eventos e uma fila
// observadora (assinada no tópico com raw delivery) com nomes únicos, então pode rodar em
// paralelo a outras instâncias e não depende do init-aws.sh.
//
//	TEST_DB_HOST=localhost TEST_DB_PORT=55433 AWS_ENDPOINT_URL=http://localhost:4567 \
//	  go test -tags component -count=1 ./test/component/...
package component

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/cmd/api/bootstrap"
	"github.com/Kc1t/postech-tc4-billing-service/config"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/awsx"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	jwtSecret     = "component-secret"
	ownerDocument = "52998224725"
	webhookSecret = "component-webhook-secret"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- infraestrutura efêmera ----------

type infra struct {
	sqs         *sqs.Client
	sns         *sns.Client
	commandsURL string
	topicARN    string
	observerURL string
	dsn         string
}

func setupInfra(t *testing.T, ctx context.Context) *infra {
	t.Helper()
	for k, v := range map[string]string{
		"AWS_ENDPOINT_URL": "http://localhost:4566", "AWS_REGION": "us-east-1",
		"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
	} {
		if os.Getenv(k) == "" {
			t.Setenv(k, v)
		}
	}
	awsCfg, err := awsx.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	in := &infra{sqs: sqs.NewFromConfig(awsCfg), sns: sns.NewFromConfig(awsCfg)}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")

	queue := func(name string) string {
		out, err := in.sqs.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name + "-" + suffix)})
		if err != nil {
			t.Skipf("LocalStack indisponível em %s: %v", os.Getenv("AWS_ENDPOINT_URL"), err)
		}
		url := aws.ToString(out.QueueUrl)
		t.Cleanup(func() {
			_, _ = in.sqs.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
		})
		return url
	}
	in.commandsURL = queue("billing-commands")
	in.observerURL = queue("billing-events-observer")

	topic, err := in.sns.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("billing-events-" + suffix)})
	if err != nil {
		t.Fatalf("criar tópico: %v", err)
	}
	in.topicARN = aws.ToString(topic.TopicArn)
	t.Cleanup(func() {
		_, _ = in.sns.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: aws.String(in.topicARN)})
	})

	attrs, err := in.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(in.observerURL), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatalf("arn da fila observadora: %v", err)
	}
	if _, err := in.sns.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(in.topicARN), Protocol: aws.String("sqs"), Endpoint: aws.String(attrs.Attributes["QueueArn"]),
		Attributes: map[string]string{"RawMessageDelivery": "true"},
	}); err != nil {
		t.Fatalf("assinar fila observadora: %v", err)
	}

	in.dsn = createDatabase(t, "billing_component_"+suffix)
	return in
}

// createDatabase cria um banco descartável no Postgres de teste (TEST_DB_*) e o remove no fim.
func createDatabase(t *testing.T, name string) string {
	t.Helper()
	host, port := getenv("TEST_DB_HOST", "localhost"), getenv("TEST_DB_PORT", "5432")
	user, pass := getenv("TEST_DB_USER", "postgres"), getenv("TEST_DB_PASSWORD", "postgres")
	adminDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, getenv("TEST_DB_NAME", "postgres"))
	admin, err := gorm.Open(postgres.Open(adminDSN), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Skipf("Postgres indisponível: %v", err)
	}
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("Postgres indisponível: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)").Error
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, name)
}

// ---------- serviço em processo ----------

type service struct {
	*infra
	baseURL string
	events  *observer
}

func startService(t *testing.T, gateway string, configure func(*config.Config)) *service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	in := setupInfra(t, ctx)

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + ln.Addr().String()
	cfg := &config.Config{
		AppEnv: config.EnvDevelopment, LogLevel: "warn", NewRelicAppName: "billing-component",
		PostgresDSN: in.dsn, DBMaxOpenConns: 5, DBMaxIdleConns: 2, DBConnMaxLifetime: time.Minute,
		JWTSecret: jwtSecret, CORSAllowedOrigins: []string{"http://localhost:3000"},
		BillingCommandsQueueURL: in.commandsURL, BillingEventsTopicARN: in.topicARN, OutboxPollInterval: 100 * time.Millisecond,
		FaultInjectionEnabled: true,
		PaymentGateway:        gateway, FakeCheckoutBaseURL: baseURL,
		MPTimeout: 2 * time.Second, MPPreferenceTTL: time.Hour,
		ReadinessTimeout: 2 * time.Second, ShutdownTimeout: 5 * time.Second,
	}
	if configure != nil {
		configure(cfg)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	app, err := bootstrap.New(ctx, cfg, log)
	if err != nil {
		cancel()
		t.Fatalf("subir o serviço: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = app.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		app.Close()
	})

	s := &service{infra: in, baseURL: baseURL, events: newObserver(in.sqs, in.observerURL)}
	s.waitReady(t)
	return s
}

func (s *service) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(s.baseURL + "/health/ready"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("serviço não ficou pronto")
}

// send publica um comando na fila billing-commands como o os-service faria.
func (s *service) send(t *testing.T, msgType, osID string, payload any) string {
	t.Helper()
	env, err := contracts.NewEnvelope(msgType, "os-service", contracts.Meta{SagaID: "saga-" + osID, OSID: osID, CorrelationID: "corr-" + osID}, payload)
	if err != nil {
		t.Fatal(err)
	}
	s.sendEnvelope(t, env)
	return env.MessageID
}

func (s *service) sendEnvelope(t *testing.T, env contracts.Envelope) {
	t.Helper()
	body, _ := env.Marshal()
	if _, err := s.sqs.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(s.commandsURL), MessageBody: aws.String(string(body))}); err != nil {
		t.Fatalf("enviar %s: %v", env.Type, err)
	}
}

func (s *service) http(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, s.baseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func token(t *testing.T, role, document string) string {
	t.Helper()
	claims := jwt.MapClaims{"sub": role + "-1", "role": role, "exp": time.Now().Add(time.Hour).Unix()}
	if document != "" {
		claims["document"] = document
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---------- observador de eventos ----------

type observer struct {
	mu   sync.Mutex
	seen []contracts.Envelope
	sqs  *sqs.Client
	url  string
}

func newObserver(client *sqs.Client, url string) *observer {
	return &observer{sqs: client, url: url}
}

// await espera um evento do tipo e da OS indicados e o devolve (cada evento é entregue uma vez).
func (o *observer) await(t *testing.T, msgType, osID string) contracts.Envelope {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if env, ok := o.take(msgType, osID); ok {
			return env
		}
		out, err := o.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(o.url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
		})
		if err != nil {
			t.Fatalf("ler fila observadora: %v", err)
		}
		for _, m := range out.Messages {
			env, err := contracts.Parse([]byte(aws.ToString(m.Body)))
			if err != nil {
				t.Fatalf("evento fora do envelope: %v", err)
			}
			o.mu.Lock()
			o.seen = append(o.seen, env)
			o.mu.Unlock()
			_, _ = o.sqs.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(o.url), ReceiptHandle: m.ReceiptHandle})
		}
	}
	t.Fatalf("evento %s da OS %s não chegou; vistos: %v", msgType, osID, o.types())
	return contracts.Envelope{}
}

func (o *observer) take(msgType, osID string) (contracts.Envelope, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, e := range o.seen {
		if e.Type == msgType && e.OSID == osID {
			o.seen = append(o.seen[:i], o.seen[i+1:]...)
			return e, true
		}
	}
	return contracts.Envelope{}, false
}

func (o *observer) types() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, e := range o.seen {
		out = append(out, e.Type+"("+e.OSID+")")
	}
	return out
}

func decode[T any](t *testing.T, env contracts.Envelope) T {
	t.Helper()
	v, err := contracts.Decode[T](env)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func createBudget(osID string, failure string) contracts.CreateBudget {
	return contracts.CreateBudget{
		OSCode:    42,
		Requester: contracts.Requester{ID: "r-1", Name: "Ana Souza", Email: "ana@example.com", Document: "529.982.247-25"},
		Items: []contracts.Item{
			{Kind: contracts.ItemKindService, Ref: "10", Description: "Troca de óleo", Quantity: 1, UnitPrice: 120.5},
			{Kind: contracts.ItemKindPart, Ref: "FIL-1", Description: "Filtro de óleo", Quantity: 2, UnitPrice: 30},
		},
		Total:           180.5,
		SimulateFailure: failure,
	}
}
