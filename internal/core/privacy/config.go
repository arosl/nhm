package privacy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/BurntSushi/toml"
)

// LoadConfig reads the [privacy] section of nhm's config file at path,
// over DefaultConfig: a key that is not set keeps ntm's default, and a
// missing file is the defaults. Other sections belong to other parts of
// nhm and are ignored here.
func LoadConfig(path string) (Config, error) {
	c := struct {
		Privacy Config `toml:"privacy"`
	}{DefaultConfig()}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c.Privacy, nil
	}
	if err != nil {
		return DefaultConfig(), err
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return DefaultConfig(), fmt.Errorf("%s: %w", path, err)
	}
	return c.Privacy, nil
}
