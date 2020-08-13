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

package configtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/prometheus/common/config"
)

var errFailNow = errors.New("fail now")

type fakeT struct {
	testing.TB
	errors []error
}

func (t *fakeT) Errorf(format string, args ...interface{}) {
	t.errors = append(t.errors, fmt.Errorf(format, args...))
}

func (t *fakeT) Fatalf(format string, args ...interface{}) {
	t.Errorf(format, args...)
	t.FailNow()
}

func (t *fakeT) FailNow() { panic(errFailNow) }

func (t *fakeT) Helper() {}

type root struct {
	Child    config.DirectorySetter
	Cyclic   *cyclic
	Map      setterMap
	Slice    setterSlice
	RootFile string
}

func (v *root) SetDirectory(dir string) {
	v.Child.SetDirectory(dir)
	v.Cyclic.SetDirectory(dir)
	v.Map.SetDirectory(dir)
	v.Slice.SetDirectory(dir)
	v.RootFile = config.JoinDir(dir, v.RootFile)
}

type cyclic struct {
	Self       *cyclic
	CyclicFile string
}

func (v *cyclic) SetDirectory(dir string) {
	if v == nil {
		return
	}
	v.CyclicFile = config.JoinDir(dir, v.CyclicFile)
}

type setterMap map[string]config.DirectorySetter

func (v setterMap) SetDirectory(dir string) {
	for _, c := range v {
		if c != nil {
			c.SetDirectory(dir)
		}
	}
}

type setterSlice []config.DirectorySetter

func (v setterSlice) SetDirectory(dir string) {
	for _, c := range v {
		if c != nil {
			c.SetDirectory(dir)
		}
	}
}

type inner struct {
	Child   config.DirectorySetter
	Disable bool
}

func (v *inner) SetDirectory(dir string) {
	if v == nil || v.Disable || v.Child == nil {
		return
	}
	v.Child.SetDirectory(dir)
}

type fooSetter struct {
	Foo     config.Secret
	FooFile string
	Extra   string
	Disable bool
}

func (v *fooSetter) SetDirectory(dir string) {
	if v == nil || v.Disable {
		return
	}
	v.FooFile = config.JoinDir(dir, v.FooFile)
}

type barSetter struct {
	BarFiles []string
	Extra    string
	Disable  bool
}

func (v *barSetter) SetDirectory(dir string) {
	if v == nil || v.Disable {
		return
	}
	for i, f := range v.BarFiles {
		v.BarFiles[i] = config.JoinDir(dir, f)
	}
}

type excludeFile struct {
	ExcludeFile string
}

func (*excludeFile) SetDirectory(dir string) {}

type includeFile struct {
	Extra   string
	Disable bool
}

func (v *includeFile) SetDirectory(dir string) {
	if v == nil || v.Disable {
		return
	}
	v.Extra = config.JoinDir(dir, v.Extra)
}

type includeFiles struct {
	Extras  []string
	Disable bool
}

func (v *includeFiles) SetDirectory(dir string) {
	if v == nil || v.Disable {
		return
	}
	for i, f := range v.Extras {
		v.Extras[i] = config.JoinDir(dir, f)
	}
}

func assertEqual(t testing.TB, want, got interface{}) {
	t.Helper()
	if diff := cmp.Diff(want, got, sortErrs, cmpErr); diff != "" {
		t.Fatalf("unexpected diff:\n\n%v\n", diff)
	}
}

var sortErrs = cmpopts.SortSlices(errLess)
var cmpErr = cmp.Comparer(errEqual)

func errLess(x, y error) bool  { return x.Error() < y.Error() }
func errEqual(x, y error) bool { return x.Error() == y.Error() }

func Test_FieldOptions(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
		err  error
	}{
		{
			name: "include field",
			fn:   func() { IncludeField(includeFile{}, "Extra") },
		},
		{
			name: "exclude field",
			fn:   func() { ExcludeField(excludeFile{}, "ExcludeFile") },
		},
		{
			name: "include pointer field",
			fn:   func() { IncludeField(&includeFile{}, "Extra") },
		},
		{
			name: "exclude pointer field",
			fn:   func() { ExcludeField(&excludeFile{}, "ExcludeFile") },
		},
		{
			name: "include invalid type",
			fn:   func() { IncludeField(setterSlice{}, "Nope") },
			err:  errors.New(`invalid struct: configtest.setterSlice is a slice`),
		},
		{
			name: "exclude invalid type",
			fn:   func() { ExcludeField(setterSlice{}, "Nope") },
			err:  errors.New(`invalid struct: configtest.setterSlice is a slice`),
		},
		{
			name: "include invalid field",
			fn:   func() { IncludeField(includeFile{}, "Nope") },
			err:  errors.New(`invalid field: "Nope" not found in configtest.includeFile`),
		},
		{
			name: "exclude invalid field",
			fn:   func() { ExcludeField(excludeFile{}, "Nope") },
			err:  errors.New(`invalid field: "Nope" not found in configtest.excludeFile`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, tt.err, func() (err interface{}) {
				defer func() { err = recover() }()
				tt.fn()
				return nil
			}())
		})
	}
}

func Test_TestSetDirectory(t *testing.T) {
	errLoad := errors.New("load error")
	errDiff := errors.New("unexpected diff")
	assertEq := func(t testing.TB, want, got interface{}) {
		if cmp.Diff(want, got) != "" {
			t.Errorf(errDiff.Error())
		}
	}

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("unexpected error getting working directory: %v", err)
	}
	base := "config.yml"
	file := filepath.Join(dir, base)

	tests := []struct {
		name string
		load func(string) (config.DirectorySetter, error)
		opts []FieldOption
		errs []error
	}{
		{
			name: "ok",
			load: func(string) (config.DirectorySetter, error) {
				return &root{
					Child: &inner{
						Child: &fooSetter{},
					},
					Map: setterMap{
						"foo": &fooSetter{},
						"bar": &barSetter{},
						"inner": &inner{
							Child: setterSlice{
								&fooSetter{},
								&barSetter{},
							},
						},
						"nil": nil,
					},
					Slice: setterSlice{
						&fooSetter{},
						&barSetter{},
						&excludeFile{},
						&includeFile{},
						&includeFiles{},
						nil,
					},
				}, nil
			},
			opts: []FieldOption{
				ExcludeField(excludeFile{}, "ExcludeFile"),
				IncludeField(includeFile{}, "Extra"),
				IncludeField(includeFiles{}, "Extras"),
			},
		},
		{
			name: "error loading file once",
			load: func(string) (config.DirectorySetter, error) { return nil, errLoad },
			errs: []error{
				fmt.Errorf("unexpected error loading file: %v: %v", file, errLoad),
			},
		},
		{
			name: "error loading file twice",
			load: func() LoadConfigFunc {
				i := 0
				return func(string) (config.DirectorySetter, error) {
					if i++; i > 1 {
						return nil, errLoad
					}
					return &fooSetter{}, nil
				}
			}(),
			errs: []error{
				fmt.Errorf("unexpected error loading file: %v: %v", file, errLoad),
			},
		},
		{
			name: "string not set",
			load: func(string) (config.DirectorySetter, error) {
				return &fooSetter{Disable: true}, nil
			},
			errs: []error{
				fmt.Errorf("(*configtest.fooSetter).FooFile = %q; want: %q", base, file),
			},
		},
		{
			name: "slice not set",
			load: func(string) (config.DirectorySetter, error) {
				return &barSetter{Disable: true}, nil
			},
			errs: []error{
				fmt.Errorf("(*configtest.barSetter).BarFiles[0] = %q; want: %q", base, file),
			},
		},
		{
			name: "included string not set",
			load: func(string) (config.DirectorySetter, error) {
				return &includeFile{Disable: true}, nil
			},
			opts: []FieldOption{
				IncludeField(includeFile{}, "Extra"),
			},
			errs: []error{
				fmt.Errorf("(*configtest.includeFile).Extra = %q; want: %q", base, file),
			},
		},
		{
			name: "included slice not set",
			load: func(string) (config.DirectorySetter, error) {
				return &includeFiles{Disable: true}, nil
			},
			opts: []FieldOption{
				IncludeField(includeFiles{}, "Extras"),
			},
			errs: []error{
				fmt.Errorf("(*configtest.includeFiles).Extras[0] = %q; want: %q", base, file),
			},
		},
		{
			name: "SetDirectory not propagated",
			load: func(string) (config.DirectorySetter, error) {
				return &inner{
					Child:   &fooSetter{},
					Disable: true,
				}, nil
			},
			errs: []error{
				errDiff,
				fmt.Errorf("(*configtest.inner).Child.(*configtest.fooSetter).FooFile = %q; want: %q", base, file),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, tt.errs, func() (errors []error) {
				t := &fakeT{}
				defer func() {
					if err := recover(); err != nil && err != errFailNow {
						panic(err)
					}
					errors = t.errors
				}()
				TestSetDirectory(t, file, tt.load, assertEq, tt.opts...)
				return t.errors
			}())
		})
	}
}

func Test_AssertFile(t *testing.T) {
	const (
		dir     = "/data/foo/bar"
		relPath = "hello/file"
		absPath = dir + "/" + relPath
	)

	cycle := &cyclic{CyclicFile: absPath}
	cycle.Self = cycle

	tests := []struct {
		name string
		root *root
		opts []FieldOption
		errs []error
	}{
		{
			name: "ok",
			root: &root{
				Child: &inner{
					Child: &fooSetter{FooFile: absPath},
				},
				Cyclic: cycle,
				Map: setterMap{
					"foo": &fooSetter{FooFile: absPath},
					"bar": &barSetter{BarFiles: []string{absPath}},
					"inner": &inner{
						Child: setterSlice{
							&fooSetter{FooFile: absPath},
							&barSetter{BarFiles: []string{absPath}},
						},
					},
					"nil": nil,
				},
				Slice: setterSlice{
					&fooSetter{FooFile: absPath},
					&barSetter{BarFiles: []string{absPath}},
					&excludeFile{},
					&includeFile{Extra: absPath},
					&includeFiles{Extras: []string{absPath}},
					nil,
				},
				RootFile: absPath,
			},
			opts: []FieldOption{
				ExcludeField(excludeFile{}, "ExcludeFile"),
				IncludeField(includeFile{}, "Extra"),
				IncludeField(includeFiles{}, "Extras"),
			},
		},
		{
			name: "basic",
			root: &root{
				RootFile: relPath,
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).RootFile = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "nested",
			root: &root{
				Child:    &fooSetter{FooFile: relPath},
				RootFile: absPath,
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Child.(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "deeply nested",
			root: &root{
				Child: &inner{
					Child: &fooSetter{FooFile: relPath},
				},
				RootFile: absPath,
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Child.(*configtest.inner).Child.(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "slice value",
			root: &root{
				Slice: setterSlice{
					&fooSetter{FooFile: absPath},
					&fooSetter{FooFile: relPath},
				},
				RootFile: absPath,
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Slice[1].(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "multiple slice values",
			root: &root{
				Slice: setterSlice{
					&fooSetter{FooFile: absPath},
					&fooSetter{FooFile: relPath},
					&fooSetter{FooFile: absPath},
					&fooSetter{FooFile: relPath},
				},
				RootFile: absPath,
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Slice[1].(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
				fmt.Errorf("(*configtest.root).Slice[3].(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "map value",
			root: &root{
				Map: setterMap{
					"foo": &fooSetter{FooFile: relPath},
				},
				RootFile: absPath,
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Map[foo].(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "multiple map values",
			root: &root{
				Map: setterMap{
					"a": &fooSetter{FooFile: relPath},
					"b": &fooSetter{FooFile: relPath},
					"c": &fooSetter{FooFile: relPath},
					"d": &fooSetter{FooFile: absPath},
					"e": &fooSetter{FooFile: absPath},
				},
				RootFile: absPath,
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Map[a].(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
				fmt.Errorf("(*configtest.root).Map[b].(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
				fmt.Errorf("(*configtest.root).Map[c].(*configtest.fooSetter).FooFile = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "include string field",
			root: &root{
				Child:    &includeFile{Extra: relPath},
				RootFile: absPath,
			},
			opts: []FieldOption{
				IncludeField(includeFile{}, "Extra"),
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Child.(*configtest.includeFile).Extra = %q; want: %q", relPath, absPath),
			},
		},
		{
			name: "include slice field",
			root: &root{
				Child: &includeFiles{
					Extras: []string{relPath},
				},
				RootFile: absPath,
			},
			opts: []FieldOption{
				IncludeField(includeFiles{}, "Extras"),
			},
			errs: []error{
				fmt.Errorf("(*configtest.root).Child.(*configtest.includeFiles).Extras[0] = %q; want: %q", relPath, absPath),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, tt.errs, func() (errors []error) {
				t := &fakeT{}
				defer func() {
					if err := recover(); err != nil && err != errFailNow {
						panic(err)
					}
					errors = t.errors
				}()
				AssertFile(t, tt.root, absPath, tt.opts...)
				return t.errors
			}())
		})
	}
}

func Test_SetFile(t *testing.T) {
	const path = "hello/file"

	want := &root{
		Child: &inner{
			Child: &fooSetter{FooFile: path},
		},
		Cyclic: &cyclic{CyclicFile: path},
		Map: setterMap{
			"foo": &fooSetter{FooFile: path},
			"bar": &barSetter{BarFiles: []string{path}},
			"inner": &inner{
				Child: setterSlice{
					&fooSetter{FooFile: path},
					&barSetter{BarFiles: []string{path}},
				},
			},
			"nil": nil,
		},
		Slice: setterSlice{
			&fooSetter{FooFile: path},
			&barSetter{BarFiles: []string{path}},
			&excludeFile{},
			&includeFile{Extra: path},
			&includeFiles{Extras: []string{path}},
			nil,
		},
		RootFile: path,
	}
	want.Cyclic.Self = want.Cyclic

	got := &root{
		Child: &inner{
			Child: &fooSetter{Foo: "<secret>"},
		},
		Cyclic: &cyclic{},
		Map: setterMap{
			"foo": &fooSetter{},
			"bar": &barSetter{},
			"inner": &inner{
				Child: setterSlice{
					&fooSetter{},
					&barSetter{},
				},
			},
			"nil": nil,
		},
		Slice: setterSlice{
			&fooSetter{},
			&barSetter{},
			&excludeFile{},
			&includeFile{},
			&includeFiles{},
			nil,
		},
	}
	got.Cyclic.Self = got.Cyclic
	SetFile(got, path,
		ExcludeField(excludeFile{}, "ExcludeFile"),
		IncludeField(includeFile{}, "Extra"),
		IncludeField(includeFiles{}, "Extras"),
	)

	assertEqual(t, want, got)
}

func Test_SetDirectory(t *testing.T) {
	const (
		dir     = "/data/foo/bar"
		relPath = "hello/file"
		absPath = dir + "/" + relPath
	)

	want := &root{
		Child: &inner{
			Child: &fooSetter{FooFile: absPath},
		},
		Cyclic: &cyclic{CyclicFile: absPath},
		Map: setterMap{
			"foo": &fooSetter{FooFile: absPath},
			"bar": &barSetter{BarFiles: []string{absPath}},
			"inner": &inner{
				Child: setterSlice{
					&fooSetter{FooFile: absPath},
					&barSetter{BarFiles: []string{absPath}},
				},
			},
			"nil": nil,
		},
		Slice: setterSlice{
			&fooSetter{FooFile: absPath},
			&barSetter{BarFiles: []string{absPath}},
			&excludeFile{},
			&includeFile{Extra: absPath},
			&includeFiles{Extras: []string{absPath}},
			nil,
		},
		RootFile: absPath,
	}
	want.Cyclic.Self = want.Cyclic

	got := &root{
		Child: &inner{
			Child: &fooSetter{FooFile: relPath},
		},
		Cyclic: &cyclic{CyclicFile: relPath},
		Map: setterMap{
			"foo": &fooSetter{FooFile: relPath},
			"bar": &barSetter{BarFiles: []string{relPath}},
			"inner": &inner{
				Child: setterSlice{
					&fooSetter{FooFile: relPath},
					&barSetter{BarFiles: []string{relPath}},
				},
			},
			"nil": nil,
		},
		Slice: setterSlice{
			&fooSetter{FooFile: relPath},
			&barSetter{BarFiles: []string{relPath}},
			&excludeFile{},
			&includeFile{Extra: relPath},
			&includeFiles{Extras: []string{relPath}},
			nil,
		},
		RootFile: relPath,
	}
	got.Cyclic.Self = got.Cyclic
	SetDirectory(got, dir)

	assertEqual(t, want, got)
}
