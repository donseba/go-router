package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

func DecodeJSON(r *http.Request, v any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

func JSON(w http.ResponseWriter, statusCode int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	return json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, statusCode int, err error) error {
	message := http.StatusText(statusCode)
	if err != nil {
		message = err.Error()
	}

	return JSON(w, statusCode, map[string]any{
		"error": message,
	})
}

func BadRequest(w http.ResponseWriter, err error) error {
	if err == nil {
		err = errors.New(http.StatusText(http.StatusBadRequest))
	}

	return Error(w, http.StatusBadRequest, err)
}

func InternalServerError(w http.ResponseWriter, err error) error {
	if err == nil {
		err = errors.New(http.StatusText(http.StatusInternalServerError))
	}

	return Error(w, http.StatusInternalServerError, err)
}

func MessageError(message string) error {
	return fmt.Errorf("%s", message)
}
