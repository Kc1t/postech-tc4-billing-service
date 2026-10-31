// Package migrations embute as migrations SQL versionadas do billing-service.
//
// Cada arquivo NNNN_descricao.sql é aplicado uma única vez, em ordem, na inicialização do
// serviço (internal/adapters/outbound/postgresql.Migrate). Nunca edite um arquivo já aplicado:
// crie a próxima versão.
package migrations

import "embed"

// FS contém os arquivos *.sql.
//
//go:embed *.sql
var FS embed.FS
