/*
Copyright 2019 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mysql

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"

	"vitess.io/vitess/go/vt/sqlparser"
	"vitess.io/vitess/go/vt/vterrors"

	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
)

// SQLError is the error structure returned from calling a db library function
type SQLError struct {
	Num     int
	State   string
	Message string
	Query   string
}

// NewSQLError creates a new SQLError.
// If sqlState is left empty, it will default to "HY000" (general error).
// TODO: Should be aligned with vterrors, stack traces and wrapping
func NewSQLError(number int, sqlState string, format string, args ...interface{}) *SQLError {
	if sqlState == "" {
		sqlState = SSUnknownSQLState
	}
	return &SQLError{
		Num:     number,
		State:   sqlState,
		Message: fmt.Sprintf(format, args...),
	}
}

// Error implements the error interface
func (se *SQLError) Error() string {
	buf := &bytes.Buffer{}
	buf.WriteString(se.Message)

	// Add MySQL errno and SQLSTATE in a format that we can later parse.
	// There's no avoiding string parsing because all errors
	// are converted to strings anyway at RPC boundaries.
	// See NewSQLErrorFromError.
	fmt.Fprintf(buf, " (errno %v) (sqlstate %v)", se.Num, se.State)

	if se.Query != "" {
		fmt.Fprintf(buf, " during query: %s", sqlparser.TruncateForLog(se.Query))
	}

	return buf.String()
}

// Number returns the internal MySQL error code.
func (se *SQLError) Number() int {
	return se.Num
}

// SQLState returns the SQLSTATE value.
func (se *SQLError) SQLState() string {
	return se.State
}

var errExtract = regexp.MustCompile(`.*\(errno ([0-9]*)\) \(sqlstate ([0-9a-zA-Z]{5})\).*`)

// NewSQLErrorFromError returns a *SQLError from the provided error.
// If it's not the right type, it still tries to get it from a regexp.
func NewSQLErrorFromError(err error) error {
	if err == nil {
		return nil
	}

	if serr, ok := err.(*SQLError); ok {
		return serr
	}

	sErr := convertToMysqlError(err)
	if _, ok := sErr.(*SQLError); ok {
		return sErr
	}

	msg := err.Error()
	match := errExtract.FindStringSubmatch(msg)
	if len(match) < 2 {
		// Map vitess error codes into the mysql equivalent
		code := vterrors.Code(err)
		num := ERUnknownError
		ss := SSUnknownSQLState
		switch code {
		case vtrpcpb.Code_CANCELED, vtrpcpb.Code_DEADLINE_EXCEEDED, vtrpcpb.Code_ABORTED:
			num = ERQueryInterrupted
			ss = SSQueryInterrupted
		case vtrpcpb.Code_UNKNOWN, vtrpcpb.Code_INVALID_ARGUMENT, vtrpcpb.Code_NOT_FOUND, vtrpcpb.Code_ALREADY_EXISTS,
			vtrpcpb.Code_FAILED_PRECONDITION, vtrpcpb.Code_OUT_OF_RANGE, vtrpcpb.Code_UNAVAILABLE, vtrpcpb.Code_DATA_LOSS:
			num = ERUnknownError
		case vtrpcpb.Code_PERMISSION_DENIED, vtrpcpb.Code_UNAUTHENTICATED:
			num = ERAccessDeniedError
			ss = SSAccessDeniedError
		case vtrpcpb.Code_RESOURCE_EXHAUSTED:
			num = demuxResourceExhaustedErrors(err.Error())
			ss = SSSyntaxErrorOrAccessViolation
		case vtrpcpb.Code_UNIMPLEMENTED:
			num = ERNotSupportedYet
			ss = SSSyntaxErrorOrAccessViolation
		case vtrpcpb.Code_INTERNAL:
			num = ERInternalError
			ss = SSUnknownSQLState
		}

		// Not found, build a generic SQLError.
		return &SQLError{
			Num:     num,
			State:   ss,
			Message: msg,
		}
	}

	num, err := strconv.Atoi(match[1])
	if err != nil {
		return &SQLError{
			Num:     ERUnknownError,
			State:   SSUnknownSQLState,
			Message: msg,
		}
	}

	serr := &SQLError{
		Num:     num,
		State:   match[2],
		Message: msg,
	}
	return serr
}

func convertToMysqlError(err error) error {
	errState := vterrors.ErrState(err)
	if errState == vterrors.Undefined {
		return err
	}
	switch errState {
	case vterrors.DataOutOfRange:
		err = NewSQLError(ERDataOutOfRange, SSDataOutOfRange, err.Error())
	case vterrors.NoDB:
		err = NewSQLError(ERNoDb, SSNoDB, err.Error())
	case vterrors.WrongNumberOfColumnsInSelect:
		err = NewSQLError(ERWrongNumberOfColumnsInSelect, SSWrongNumberOfColumns, err.Error())
	case vterrors.BadFieldError:
		err = NewSQLError(ERBadFieldError, SSBadFieldError, err.Error())
	}
	return err
}

var isGRPCOverflowRE = regexp.MustCompile(`.*grpc: received message larger than max \(\d+ vs. \d+\)`)

func demuxResourceExhaustedErrors(msg string) int {
	switch {
	case isGRPCOverflowRE.Match([]byte(msg)):
		return ERNetPacketTooLarge
	default:
		return ERTooManyUserConnections
	}
}
