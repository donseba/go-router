package router

import (
	"net/http"
	"strconv"
)

func Param(r *http.Request, name string) string {
	return r.PathValue(name)
}

func IntParam(r *http.Request, name string) (int, error) {
	return strconv.Atoi(Param(r, name))
}

func Int64Param(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(Param(r, name), 10, 64)
}

func BoolParam(r *http.Request, name string) (bool, error) {
	return strconv.ParseBool(Param(r, name))
}

func Float64Param(r *http.Request, name string) (float64, error) {
	return strconv.ParseFloat(Param(r, name), 64)
}
