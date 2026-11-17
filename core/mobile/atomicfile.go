package mobile

import "os"

// atomicWriteFile replaces the file at path with data atomically:
// write a temp file in the same directory, fsync, then rename over
// the target. A kill or power loss mid-write can no longer leave a
// truncated file behind — the same discipline accounts_pool.json
// already uses. settings.json and events.json used to be rewritten
// in place with os.WriteFile; a crash at the wrong moment silently
// reset the user's location/bypass choices to defaults.
func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
