package pg

import (
	"sync"
	"time"

	"gopkg.in/pg.v4/internal/pool"
	"gopkg.in/pg.v4/types"
)

// Stmt is a prepared statement. Stmt is safe for concurrent use by
// multiple goroutines.
type Stmt struct {
	db *DB

	mu  sync.Mutex
	_cn *pool.Conn

	name    string
	columns []string

	err error
}

func prepare(db *DB, cn *pool.Conn, q string) (*Stmt, error) {
	name := cn.NextId()
	writeParseDescribeSyncMsg(cn.Wr, name, q)
	if err := cn.Wr.Flush(); err != nil {
		db.freeConn(cn, err)
		return nil, err
	}

	columns, err := readParseDescribeSync(cn)
	if err != nil {
		db.freeConn(cn, err)
		return nil, err
	}

	stmt := &Stmt{
		db:      db,
		_cn:     cn,
		name:    name,
		columns: columns,
	}
	return stmt, nil
}

// Prepare creates a prepared statement for later queries or
// executions. Multiple queries or executions may be run concurrently
// from the returned statement.
func (db *DB) Prepare(q string) (*Stmt, error) {
	cn, err := db.conn()
	if err != nil {
		return nil, err
	}
	return prepare(db, cn, q)
}

func (stmt *Stmt) conn() (*pool.Conn, error) {
	if stmt._cn == nil {
		return nil, errStmtClosed
	}
	stmt._cn.SetReadTimeout(stmt.db.opt.ReadTimeout)
	stmt._cn.SetWriteTimeout(stmt.db.opt.WriteTimeout)
	return stmt._cn, nil
}

func (stmt *Stmt) exec(params ...interface{}) (*types.Result, error) {
	defer stmt.mu.Unlock()
	stmt.mu.Lock()

	cn, err := stmt.conn()
	if err != nil {
		return nil, err
	}
	return extQuery(cn, stmt.name, params...)
}

// Exec executes a prepared statement with the given parameters.
func (stmt *Stmt) Exec(params ...interface{}) (res *types.Result, err error) {
	for i := 0; i < 3; i++ {
		res, err = stmt.exec(params...)

		if i >= stmt.db.opt.MaxRetries {
			break
		}
		if !shouldRetry(err) {
			break
		}

		time.Sleep(defaultBackoff << uint(i))
	}
	if err != nil {
		stmt.setErr(err)
	}
	return
}

// ExecOne acts like Exec, but query must affect only one row. It
// returns ErrNoRows error when query returns zero rows or
// ErrMultiRows when query returns multiple rows.
func (stmt *Stmt) ExecOne(params ...interface{}) (*types.Result, error) {
	res, err := stmt.Exec(params...)
	if err != nil {
		return nil, err
	}
	return assertOneAffected(res)
}

func (stmt *Stmt) query(model interface{}, params ...interface{}) (*types.Result, error) {
	defer stmt.mu.Unlock()
	stmt.mu.Lock()

	cn, err := stmt.conn()
	if err != nil {
		return nil, err
	}
	return extQueryData(cn, stmt.name, model, stmt.columns, params...)
}

// Query executes a prepared query statement with the given parameters.
func (stmt *Stmt) Query(model interface{}, params ...interface{}) (res *types.Result, err error) {
	for i := 0; i < 3; i++ {
		res, err = stmt.query(model, params...)

		if i >= stmt.db.opt.MaxRetries {
			break
		}
		if !shouldRetry(err) {
			break
		}

		time.Sleep(defaultBackoff << uint(i))
	}
	if err != nil {
		stmt.setErr(err)
	}
	return
}

// QueryOne acts like Query, but query must return only one row. It
// returns ErrNoRows error when query returns zero rows or
// ErrMultiRows when query returns multiple rows.
func (stmt *Stmt) QueryOne(model interface{}, params ...interface{}) (*types.Result, error) {
	mod, err := newSingleModel(model)
	if err != nil {
		return nil, err
	}
	res, err := stmt.Query(mod, params...)
	if err != nil {
		return nil, err
	}
	return assertOneAffected(res)
}

func (stmt *Stmt) setErr(e error) {
	stmt.err = e
}

// Close closes the statement.
func (stmt *Stmt) Close() error {
	if stmt._cn == nil {
		return errStmtClosed
	}
	err := stmt.db.freeConn(stmt._cn, stmt.err)
	stmt._cn = nil
	return err
}

func extQuery(cn *pool.Conn, name string, params ...interface{}) (*types.Result, error) {
	if err := writeBindExecuteMsg(cn.Wr, name, params...); err != nil {
		return nil, err
	}
	if err := cn.Wr.Flush(); err != nil {
		return nil, err
	}
	return readExtQuery(cn)
}

func extQueryData(cn *pool.Conn, name string, model interface{}, columns []string, params ...interface{}) (*types.Result, error) {
	if err := writeBindExecuteMsg(cn.Wr, name, params...); err != nil {
		return nil, err
	}

	if err := cn.Wr.Flush(); err != nil {
		return nil, err
	}

	return readExtQueryData(cn, model, columns)
}
