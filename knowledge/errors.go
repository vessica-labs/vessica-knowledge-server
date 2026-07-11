package knowledge

import "fmt"

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func Invalid(message string) error {
	return &Error{Code: "invalid_request", Message: message, Status: 400}
}
func NotFound(message string) error { return &Error{Code: "not_found", Message: message, Status: 404} }
func Conflict(message string) error { return &Error{Code: "conflict", Message: message, Status: 409} }
