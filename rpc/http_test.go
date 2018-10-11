// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPErrorResponseWithDelete(t *testing.T) {
	testHTTPErrorResponse(t, http.MethodDelete, contentType, "", http.StatusMethodNotAllowed)
}

func TestHTTPErrorResponseWithPut(t *testing.T) {
	testHTTPErrorResponse(t, http.MethodPut, contentType, "", http.StatusMethodNotAllowed)
}

func TestHTTPErrorResponseWithMaxContentLength(t *testing.T) {
	body := make([]rune, maxRequestContentLength+1)
	testHTTPErrorResponse(t,
		http.MethodPost, contentType, string(body), http.StatusRequestEntityTooLarge)
}

func TestHTTPErrorResponseWithEmptyContentType(t *testing.T) {
	testHTTPErrorResponse(t, http.MethodPost, "", "", http.StatusUnsupportedMediaType)
}

func TestHTTPErrorResponseWithValidRequest(t *testing.T) {
	testHTTPErrorResponse(t, http.MethodPost, contentType, "", 0)
}

func testHTTPErrorResponse(t *testing.T, method, contentType, body string, expected int) {
	request := httptest.NewRequest(method, "http://url.com", strings.NewReader(body))
	request.Header.Set("content-type", contentType)
	if code, _ := validateRequest(request); code != expected {
		t.Fatalf("response code should be %d not %d", expected, code)
	}
}

func TestVirtualHostHandler_validHost(t *testing.T) {
	for _, test := range []struct {
		name    string
		vhosts  []string
		valid   []string
		invalid []string
	}{
		{
			name:   "any",
			vhosts: []string{"*"},
			valid:  []string{"google.com", "example.com", "gochain.io"},
		},
		{
			name:    "literal",
			vhosts:  []string{"test.gochain.io"},
			valid:   []string{"test.gochain.io"},
			invalid: []string{"a.gochain.io", "a.test.gochain.io"},
		},
		{
			name:    "subdomain",
			vhosts:  []string{"*.gochain.io"},
			valid:   []string{"test.gochain.io", "a.gochain.io", "a.b.gochain.io"},
			invalid: []string{"gochain.io"},
		},
		{
			name:    "multi",
			vhosts:  []string{"*.e.d.c.b.a"},
			valid:   []string{"f.e.d.c.b.a"},
			invalid: []string{"e.d.c.b.a", "c.b.a", "b.a"},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			h := newVHostHandler(test.vhosts, nil)
			for _, host := range test.valid {
				if !h.validHost(host) {
					t.Errorf("expected %q to be valid", host)
				}
			}
			for _, host := range test.invalid {
				if h.validHost(host) {
					t.Errorf("expected %q to be invalid", host)
				}
			}
		})
	}
}
