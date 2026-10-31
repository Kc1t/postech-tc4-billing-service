package entities

// Role é o papel de quem chama a API, vindo da claim "role" do JWT.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleMechanic Role = "mechanic"
	RoleClient   Role = "client"
)

// IsValid diz se o papel é conhecido.
func (r Role) IsValid() bool {
	switch r {
	case RoleAdmin, RoleMechanic, RoleClient:
		return true
	}
	return false
}

// Actor é o usuário autenticado que dispara um caso de uso pela API.
type Actor struct {
	UserID   string
	Role     Role
	Document string // CPF do cliente; vazio para a equipe da oficina
}

// IsAdmin diz se o ator é da administração da oficina.
func (a Actor) IsAdmin() bool { return a.Role == RoleAdmin }

// Owns diz se o ator é o cliente dono do documento informado.
func (a Actor) Owns(document string) bool {
	mine := NormalizeDocument(a.Document)
	return a.Role == RoleClient && mine != "" && mine == NormalizeDocument(document)
}
