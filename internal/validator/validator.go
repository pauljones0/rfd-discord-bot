package validator

import (
	"fmt"

	"github.com/go-playground/validator/v10"
)

// Validator is a wrapper around the validator library.
type Validator struct {
	validate *validator.Validate
}

// New creates a new Validator instance.
func New() *Validator {
	return &Validator{
		validate: validator.New(),
	}
}

// ValidateStruct validates a struct based on its tags.
func (v *Validator) ValidateStruct(s interface{}) error {
	err := v.validate.Struct(s)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	return nil
}
