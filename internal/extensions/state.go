package extensions

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/getan/golder/internal/golderhome"
)

func sourceHome(ref SourceRef) string {
	return filepath.Join(golderhome.Dir(), "extensions", ref.Kind+"-"+ref.ID, "home")
}

// No symlink component in a private state path may redirect creation/reset into
// another source. The protected root itself may have a canonical OS spelling.
func privateHome(home string) error {
	root, err := filepath.EvalSymlinks(golderhome.Dir())
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err = os.MkdirAll(golderhome.Dir(), 0o700); err != nil {
			return err
		}
		root, err = filepath.EvalSymlinks(golderhome.Dir())
		if err != nil {
			return err
		}
	}
	rel, err := filepath.Rel(golderhome.Dir(), home)
	if err != nil {
		return err
	}
	p := root
	for _, part := range []string{"extensions", filepath.Base(filepath.Dir(rel)), "home"} {
		p = filepath.Join(p, part)
		if err = os.Mkdir(p, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(st) {
			return errors.New("refusing non-private extension state directory")
		}
		if err = os.Chmod(p, 0o700); err != nil {
			return err
		}
	}
	return nil
}
