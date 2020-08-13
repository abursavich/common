// Copyright 2020 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package configtest provides config testing utilities.
package configtest

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/common/config"
)

var (
	stringTyp      = reflect.TypeOf("")
	stringSliceTyp = reflect.TypeOf([]string{})
)

type set map[interface{}]bool

// A FieldOption specifies which fields to target.
type FieldOption func(*fields)

type fields struct {
	include map[field]bool
	exclude map[field]bool
}

type field struct {
	Struct reflect.Type
	Name   string
}

func structField(strukt interface{}, name string) field {
	typ := ptrType(reflect.TypeOf(strukt))
	if typ.Kind() != reflect.Struct {
		panic(fmt.Errorf("invalid struct: %v is a %v", typ, typ.Kind()))
	}
	if _, found := typ.FieldByName(name); !found {
		panic(fmt.Errorf("invalid field: %q not found in %v", name, typ))
	}
	return field{typ, name}
}

// IncludeField treats the named field in the given struct's type as if it
// does include files. This is useful if the field name does not end in
// "File" or "Files" but is intentionally affected by SetDirectory.
func IncludeField(strukt interface{}, name string) FieldOption {
	key := structField(strukt, name)
	return func(o *fields) {
		if o.include == nil {
			o.include = make(map[field]bool)
		}
		o.include[key] = true
	}
}

// ExcludeField treats the named field in the given struct's type as if it
// does not include files. This is useful if the field name ends in "File"
// or "Files" but is intentionally unaffected by SetDirectory.
func ExcludeField(strukt interface{}, name string) FieldOption {
	key := structField(strukt, name)
	return func(o *fields) {
		if o.exclude == nil {
			o.exclude = make(map[field]bool)
		}
		o.exclude[key] = true
	}
}

// LoadConfigFunc loads the given file as a config.
type LoadConfigFunc func(file string) (config.DirectorySetter, error)

// AssertEqualFunc asserts that the given values are equal
// and fails the test if they are not.
type AssertEqualFunc func(t testing.TB, want, got interface{})

// TestSetDirectory uses reflection to test that calling SetDirectory on the root
// config with an absolute path has the same effect as calling SetDirectory on all
// inner and leaf values that implement it. It also tests that calling SetDirectory
// on the root updates all fields that look like files, which includes string fields
// with names ending in "File" and []string fields with names ending in "Files"
// by default.
func TestSetDirectory(t testing.TB, file string, load LoadConfigFunc, assertEqual AssertEqualFunc, options ...FieldOption) {
	t.Helper()

	file, err := filepath.Abs(file)
	if err != nil {
		t.Fatalf("unexpected error getting absolute path: %v: %v", file, err)
	}
	dir := filepath.Dir(file)
	base := filepath.Base(file)

	want, err := load(file)
	if err != nil {
		t.Fatalf("unexpected error loading file: %v: %v", file, err)
	}
	SetFile(want, base, options...)
	SetDirectory(want, dir)

	got, err := load(file)
	if err != nil {
		t.Fatalf("unexpected error loading file: %v: %v", file, err)
	}
	SetFile(got, base, options...)
	got.SetDirectory(dir)

	assertEqual(t, want, got)
	AssertFile(t, got, file, options...)
}

// AssertFile uses reflection to assert that every field in the config that looks
// like a file matches the given path. This includes string fields with names ending
// in "File" and []string fields with names ending in "Files" by default.
// It can be used with SetFile and SetDirectory to confirm that the config's
// implementation of SetDirectory covers all files.
func AssertFile(t testing.TB, config config.DirectorySetter, path string, options ...FieldOption) {
	t.Helper()

	opts := &fields{}
	for _, fn := range options {
		fn(opts)
	}
	typ := ifaceType(reflect.ValueOf(config))
	if !assertFile(t, fmt.Sprintf("(%v)", typ), reflect.ValueOf(config), path, set{}, opts) {
		t.FailNow()
	}
}

// SetFile uses reflection to replace every field in the config that looks
// like a file with the given path. This includes string fields with names ending
// in "File" and []string fields with names ending in "Files" by default.
func SetFile(config config.DirectorySetter, path string, options ...FieldOption) {
	opts := &fields{}
	for _, fn := range options {
		fn(opts)
	}
	setFile(reflect.ValueOf(config), path, set{}, opts)
}

// SetDirectory uses reflection to call SetDirectory with dir on every value
// in the config that implements it. For best results, dir should be an
// absolute path because SetDirectory should be called on inner and leaf
// values multiple times.
func SetDirectory(config config.DirectorySetter, dir string) {
	setDirectory(reflect.ValueOf(config), dir, set{})
}

func assertFile(t testing.TB, path string, val reflect.Value, want string, seen set, opts *fields) bool {
	t.Helper()

	if isNil(val) {
		return true
	}

	switch typ := val.Type(); typ.Kind() {
	case reflect.Ptr:
		if key := val.Interface(); !seen[key] {
			seen[key] = true
			return assertFile(t, path, val.Elem(), want, seen, opts)
		}
		return true
	case reflect.Struct:
		ok := true
		for i, n := 0, typ.NumField(); i < n; i++ {
			vf := val.Field(i)
			tf := typ.Field(i)
			key := field{typ, tf.Name}
			if !vf.CanSet() || opts.exclude[key] {
				continue // Field is unexported or excluded.
			}
			switch {
			case tf.Type == stringTyp && (strings.HasSuffix(tf.Name, "File") || opts.include[key]):
				if got := vf.String(); got != want {
					t.Errorf("%s.%s = %q; want: %q", path, tf.Name, got, want)
					ok = false
				}
			case tf.Type == stringSliceTyp && (strings.HasSuffix(tf.Name, "Files") || opts.include[key]):
				for j, k := 0, vf.Len(); j < k; j++ {
					if got := vf.Index(j).String(); got != want {
						t.Errorf("%s.%s[%d] = %q; want: %q", path, tf.Name, j, got, want)
						ok = false
					}
				}
			default:
				if !assertFile(t, path+"."+tf.Name, vf, want, seen, opts) {
					ok = false
				}
			}
		}
		return ok
	case reflect.Map:
		ok := true
		for _, key := range val.MapKeys() {
			keyPath := fmt.Sprintf("(%v)", ifaceType(key))
			if !assertFile(t, keyPath, key, want, seen, opts) {
				ok = false
			}
			valPath := fmt.Sprintf("%s[%v]", path, key.Interface())
			if !assertFile(t, valPath, val.MapIndex(key), want, seen, opts) {
				ok = false
			}
		}
		return ok
	case reflect.Slice, reflect.Array:
		ok := true
		for i, n := 0, val.Len(); i < n; i++ {
			if !assertFile(t, fmt.Sprintf("%s[%d]", path, i), val.Index(i), want, seen, opts) {
				ok = false
			}
		}
		return ok
	case reflect.Interface:
		path := fmt.Sprintf("%s.(%v)", path, ifaceType(val))
		return assertFile(t, path, val.Elem(), want, seen, opts)
	default:
		return true
	}
}

func setFile(val reflect.Value, file string, seen set, opts *fields) {
	if isNil(val) {
		return
	}

	switch typ := val.Type(); typ.Kind() {
	case reflect.Ptr:
		if key := val.Interface(); !seen[key] {
			seen[key] = true
			setFile(val.Elem(), file, seen, opts)
		}
	case reflect.Struct:
		for i, n := 0, typ.NumField(); i < n; i++ {
			vf := val.Field(i)
			tf := typ.Field(i)
			key := field{typ, tf.Name}
			if !vf.CanSet() || opts.exclude[key] {
				continue // Field is unexported or excluded.
			}
			switch {
			case tf.Type == stringTyp && (strings.HasSuffix(tf.Name, "File") || opts.include[key]):
				// Clear the string field, if it exists.
				sf := val.FieldByName(strings.TrimSuffix(tf.Name, "File"))
				if sf.IsValid() && sf.Type().Kind() == reflect.String {
					// NB: Check Kind because Type may be Secret.
					sf.SetString("")
				}
				// Set the file field.
				vf.SetString(file)
			case tf.Type == stringSliceTyp && (strings.HasSuffix(tf.Name, "Files") || opts.include[key]):
				vf.Set(reflect.ValueOf([]string{file}))
			default:
				setFile(vf, file, seen, opts)
			}
		}
	case reflect.Map:
		for _, key := range val.MapKeys() {
			setFile(key, file, seen, opts)
			setFile(val.MapIndex(key), file, seen, opts)
		}
	case reflect.Slice, reflect.Array:
		for i, n := 0, val.Len(); i < n; i++ {
			setFile(val.Index(i), file, seen, opts)
		}
	case reflect.Interface:
		setFile(val.Elem(), file, seen, opts)
	}
}

func setDirectory(val reflect.Value, dir string, seen set) {
	if isNil(val) {
		return
	}

	v := val
	if val.Kind() != reflect.Ptr && val.CanAddr() {
		v = val.Addr()
	}
	if i, ok := v.Interface().(config.DirectorySetter); ok {
		i.SetDirectory(dir)
	}

	switch typ := val.Type(); typ.Kind() {
	case reflect.Ptr:
		if key := val.Interface(); !seen[key] {
			seen[key] = true
			setDirectory(val.Elem(), dir, seen)
		}
	case reflect.Struct:
		for i, n := 0, typ.NumField(); i < n; i++ {
			vf := val.Field(i)
			if !vf.CanSet() {
				continue // Field is unexported.
			}
			setDirectory(vf, dir, seen)
		}
	case reflect.Map:
		for _, key := range val.MapKeys() {
			setDirectory(key, dir, seen)
			setDirectory(val.MapIndex(key), dir, seen)
		}
	case reflect.Slice, reflect.Array:
		for i, n := 0, val.Len(); i < n; i++ {
			setDirectory(val.Index(i), dir, seen)
		}
	case reflect.Interface:
		setDirectory(val.Elem(), dir, seen)
	}
}

func isNil(val reflect.Value) bool {
	switch val.Kind() {
	case reflect.Ptr,
		reflect.Map,
		reflect.Slice,
		reflect.Interface,
		reflect.Chan,
		reflect.Func,
		reflect.UnsafePointer:
		return val.IsNil()
	default:
		return false
	}
}

func ptrType(typ reflect.Type) reflect.Type {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	return typ
}

func ifaceType(val reflect.Value) reflect.Type {
	for val.Kind() == reflect.Interface && !val.IsNil() {
		val = val.Elem()
	}
	return val.Type()
}
