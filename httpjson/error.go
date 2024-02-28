package httpjson

// ErrorMessage is a helper to create a JSON error response
// with a status code and a message.
func ErrorMessage(status int, err error) *Response {
	return &Response{
		Status: status,
		Body: M{
			// In the future, we can extend this
			// logic to present structured errors.
			"error": err.Error(),
		},
	}
}
