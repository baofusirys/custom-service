package handler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakedb_test.go [119] handler 层测试用最小 database/sql 驱动：按 SQL 片段返回结果，记录执行的写语句。
type fakeRes struct {
	cols     []string
	rows     [][]driver.Value
	affected int64
	err      error
}

type fakeExec struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	mu      sync.Mutex
	handler func(query string, args []driver.Value) fakeRes
	execs   []fakeExec
}

func openFakeDB(t *testing.T, h func(query string, args []driver.Value) fakeRes) (*sql.DB, *fakeDB) {
	t.Helper()
	f := &fakeDB{handler: h}
	db := sql.OpenDB(f)
	t.Cleanup(func() { _ = db.Close() })
	return db, f
}

func (f *fakeDB) execsMatching(sub string) []fakeExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeExec
	for _, e := range f.execs {
		if strings.Contains(e.query, sub) {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeDB) Connect(context.Context) (driver.Conn, error) { return &fakeConn{f: f}, nil }
func (f *fakeDB) Driver() driver.Driver                        { return fakeDriver{f: f} }

type fakeDriver struct{ f *fakeDB }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{f: d.f}, nil }

type fakeConn struct{ f *fakeDB }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: prepare not supported")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return fakeTx{}, nil }
func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return fakeTx{}, nil
}

func values(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	res := c.f.handler(query, values(args))
	if res.err != nil {
		return nil, res.err
	}
	return &fakeRows{cols: res.cols, rows: res.rows}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := values(args)
	c.f.mu.Lock()
	c.f.execs = append(c.f.execs, fakeExec{query: query, args: vals})
	c.f.mu.Unlock()
	res := c.f.handler(query, vals)
	if res.err != nil {
		return nil, res.err
	}
	return driver.RowsAffected(res.affected), nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}
