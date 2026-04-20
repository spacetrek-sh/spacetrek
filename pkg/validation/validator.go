package validation

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
)

var (
	once     sync.Once
	instance *validator.Validate
)

func getInstance() *validator.Validate {
	once.Do(func() {
		instance = validator.New()

		// Use the json struct tag name in error messages so field names match
		// what clients see in the JSON payload.
		instance.RegisterTagNameFunc(func(fld reflect.StructField) string {
			name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
			if name == "-" || name == "" {
				return fld.Name
			}
			return name
		})
	})
	return instance
}

// Struct validates a struct using validator struct-tag rules.
// Returns nil when valid, or an *exception.AppError (VALIDATION_ERROR) carrying
// per-field details when invalid.
func Struct(v any) error {
	err := getInstance().Struct(v)
	if err == nil {
		return nil
	}

	var valErrs validator.ValidationErrors
	if ok := isValidationErrors(err, &valErrs); !ok {
		return exception.BadRequest(err.Error())
	}

	fields := make([]exception.FieldError, 0, len(valErrs))
	for _, fe := range valErrs {
		fields = append(fields, exception.FieldError{
			Field:   fe.Field(),
			Message: fieldMessage(fe),
		})
	}
	return exception.ValidationFailed(fields)
}

// Var validates a single value against a tag string (e.g. "required,email").
// Returns a descriptive BadRequest error on failure.
func Var(v any, tag string) error {
	err := getInstance().Var(v, tag)
	if err == nil {
		return nil
	}
	return exception.BadRequest(fmt.Sprintf("value validation failed: %s", tag))
}

// isValidationErrors attempts a type-assert of err into *validator.ValidationErrors.
func isValidationErrors(err error, out *validator.ValidationErrors) bool {
	if ve, ok := err.(validator.ValidationErrors); ok {
		*out = ve
		return true
	}
	return false
}

func fieldMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "url":
		return "must be a valid URL"
	case "uuid", "uuid4":
		return "must be a valid UUID"
	case "min":
		return fmt.Sprintf("must be at least %s characters long", fe.Param())
	case "max":
		return fmt.Sprintf("must be at most %s characters long", fe.Param())
	case "len":
		return fmt.Sprintf("must be exactly %s characters long", fe.Param())
	case "gte":
		return fmt.Sprintf("must be greater than or equal to %s", fe.Param())
	case "lte":
		return fmt.Sprintf("must be less than or equal to %s", fe.Param())
	case "oneof":
		return fmt.Sprintf("must be one of [%s]", fe.Param())
	case "alphanum":
		return "must contain only alphanumeric characters"
	case "numeric":
		return "must be a numeric value"
	default:
		return fmt.Sprintf("failed validation constraint: %s", fe.Tag())
	}
}
