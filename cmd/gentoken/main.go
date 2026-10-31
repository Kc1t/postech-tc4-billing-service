// Command gentoken emite um JWT HS256 de desenvolvimento para chamar a API do billing-service
// (Postman, curl, testes manuais). Usa o mesmo JWT_SECRET do serviço.
//
//	go run ./cmd/gentoken -role admin
//	go run ./cmd/gentoken -role client -document 52998224725
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/config"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/env"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	role := flag.String("role", "admin", "papel: admin, mechanic ou client")
	sub := flag.String("sub", "dev-user", "claim sub (id do usuário)")
	document := flag.String("document", "", "CPF do cliente (claim document; obrigatório para client)")
	ttl := flag.Duration("ttl", time.Hour, "validade do token")
	flag.Parse()

	if *role == "client" && *document == "" {
		fmt.Fprintln(os.Stderr, "gentoken: -document é obrigatório para role client")
		os.Exit(2)
	}
	now := time.Now()
	claims := jwt.MapClaims{"sub": *sub, "role": *role, "iat": now.Unix(), "exp": now.Add(*ttl).Unix()}
	if *document != "" {
		claims["document"] = *document
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte(env.GetOrDefault("JWT_SECRET", config.DefaultJWTSecret)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gentoken:", err)
		os.Exit(1)
	}
	fmt.Println(token)
}
