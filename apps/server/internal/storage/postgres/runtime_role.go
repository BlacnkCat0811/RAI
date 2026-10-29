package postgres

import (
	"context"
	"errors"
)

// RuntimeRoleSafe is a release startup gate, never a migration or privilege grant.
func (s *Store) RuntimeRoleSafe(ctx context.Context) error {
	var safe bool
	err := s.Pool.QueryRow(ctx, `SELECT NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls
	 OR has_schema_privilege(current_user,'public','CREATE')
	 OR has_database_privilege(current_user,current_database(),'TEMP')
	 OR pg_has_role(current_user,d.datdba,'MEMBER'))
	 FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&safe)
	if err != nil {
		return err
	}
	if !safe {
		return errors.New("runtime role has administrative privileges")
	}
	return nil
}
