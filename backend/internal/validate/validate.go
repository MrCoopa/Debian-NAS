package validate

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	poolNameRegex    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.:-]{0,254}$`)
	datasetPartRegex = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)
	snapNameRegex    = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)
	usernameRegex    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	shareNameRegex   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,79}$`)
	devicePathRegex  = regexp.MustCompile(`^/dev/([a-zA-Z0-9._/-]+)$`)
	nfsClientRegex   = regexp.MustCompile(`^[A-Za-z0-9.*:/@_-]+$`)

	reservedPoolNames = map[string]bool{
		"mirror": true, "raidz": true, "raidz1": true, "raidz2": true, "raidz3": true,
		"draid": true, "draid1": true, "draid2": true, "draid3": true,
		"spare": true, "log": true, "cache": true, "special": true, "dedup": true,
	}

	reservedShareNames = map[string]bool{
		"global": true, "homes": true, "printers": true, "print$": true, "ipc$": true,
	}

	reservedSystemUsers = map[string]bool{
		"root": true, "daemon": true, "bin": true, "sys": true, "sync": true,
		"games": true, "man": true, "lp": true, "mail": true, "news": true,
		"uucp": true, "proxy": true, "majordomo": true, "postgres": true,
		"www-data": true, "backup": true, "list": true, "irc": true, "gnats": true,
		"nobody": true, "systemd-network": true, "systemd-resolve": true,
		"systemd-timesync": true, "messagebus": true, "sshd": true, "_apt": true,
		"admin": false, // user may create an admin role storage account
	}

	forbiddenPaths = map[string]bool{
		"/": true, "/bin": true, "/boot": true, "/dev": true, "/etc": true,
		"/home": true, "/lib": true, "/lib64": true, "/media": true,
		"/opt": true, "/proc": true, "/root": true, "/run": true,
		"/sbin": true, "/sys": true, "/tmp": true, "/usr": true, "/var": true,
	}

	allowedDatasetProps = map[string]bool{
		"compression": true, "recordsize": true, "atime": true,
		"quota": true, "refquota": true, "reservation": true, "refreservation": true,
		"readonly": true, "snapdir": true, "acltype": true, "xattr": true,
		"sync": true, "redundant_metadata": true,
	}

	allowedPoolProps = map[string]bool{
		"autotrim": true, "autoexpand": true, "failmode": true,
		"comment": true,
	}

	allowedNfsOptions = map[string]bool{
		"ro": true, "rw": true, "sync": true, "async": true,
		"wdelay": true, "no_wdelay": true, "hide": true, "nohide": true,
		"crossmnt": true, "subtree_check": true, "no_subtree_check": true,
		"insecure_locks": true, "secure_locks": true, "auth_nlm": true, "no_auth_nlm": true,
		"auth_sys": true, "no_auth_sys": true,
		"root_squash": true, "no_root_squash": true, "all_squash": true, "no_all_squash": true,
		"secure": true, "insecure": true,
	}
)

func PoolName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("pool name cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("pool name cannot start with a hyphen")
	}
	if !poolNameRegex.MatchString(name) {
		return fmt.Errorf("invalid pool name '%s': must start with a letter and contain only alphanumeric, '_', '.', ':', '-'", name)
	}
	if reservedPoolNames[strings.ToLower(name)] {
		return fmt.Errorf("pool name '%s' is a reserved ZFS keyword", name)
	}
	return nil
}

func DatasetName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("dataset name cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("dataset name cannot start with a hyphen")
	}
	if strings.Contains(name, "//") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("invalid dataset name '%s': cannot contain leading, trailing, or duplicate slashes", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid dataset name '%s': cannot contain '..'", name)
	}

	parts := strings.Split(name, "/")
	if err := PoolName(parts[0]); err != nil {
		return fmt.Errorf("invalid root pool in dataset '%s': %w", name, err)
	}

	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "-") {
			return fmt.Errorf("dataset component '%s' cannot start with a hyphen", part)
		}
		if !datasetPartRegex.MatchString(part) {
			return fmt.Errorf("invalid dataset component '%s' in '%s'", part, name)
		}
	}
	if len(name) > 255 {
		return errors.New("dataset name exceeds 255 characters")
	}
	return nil
}

func SnapshotName(dataset, snapName string) error {
	if err := DatasetName(dataset); err != nil {
		return err
	}
	snapName = strings.TrimSpace(snapName)
	if snapName == "" {
		return errors.New("snapshot name cannot be empty")
	}
	if strings.HasPrefix(snapName, "-") || strings.HasPrefix(snapName, "%") {
		return errors.New("snapshot name cannot start with a hyphen or percent sign")
	}
	if !snapNameRegex.MatchString(snapName) {
		return fmt.Errorf("invalid snapshot name '%s': contains forbidden characters", snapName)
	}
	return nil
}

func Username(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("username cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("username cannot start with a hyphen")
	}
	if !usernameRegex.MatchString(name) {
		return fmt.Errorf("invalid username '%s': must start with lowercase letter or underscore and contain only a-z, 0-9, '_', '-' (max 32 chars)", name)
	}
	lower := strings.ToLower(name)
	if reservedSystemUsers[lower] {
		return fmt.Errorf("username '%s' is a reserved Linux system account", name)
	}
	return nil
}

func Password(pw string) error {
	if len(pw) < 4 {
		return errors.New("password must be at least 4 characters")
	}
	if strings.ContainsAny(pw, "\n\r\x00") {
		return errors.New("password cannot contain newline, carriage return, or null bytes")
	}
	return nil
}

func ShareName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("share name cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("share name cannot start with a hyphen")
	}
	if !shareNameRegex.MatchString(name) {
		return fmt.Errorf("invalid share name '%s': must start with alphanumeric character and not contain brackets, slashes, or equals", name)
	}
	if reservedShareNames[strings.ToLower(name)] {
		return fmt.Errorf("share name '%s' is a reserved Samba section name", name)
	}
	return nil
}

func ShareComment(comment string) error {
	if len(comment) > 255 {
		return errors.New("comment exceeds maximum length of 255 characters")
	}
	if strings.ContainsAny(comment, "\n\r\x00") {
		return errors.New("comment cannot contain newline, carriage return, or null bytes")
	}
	return nil
}

func SharePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("share path cannot be empty")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("share path '%s' must be an absolute path starting with '/'", path)
	}
	if strings.ContainsAny(path, "\n\r\x00") {
		return errors.New("share path contains invalid control characters")
	}

	cleaned := filepath.ToSlash(filepath.Clean(path))
	if forbiddenPaths[cleaned] {
		return fmt.Errorf("share path '%s' points to a protected system directory", path)
	}

	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	if len(parts) > 0 && forbiddenPaths["/"+parts[0]] {
		return fmt.Errorf("share path '%s' cannot be created under system root directory '/%s'", path, parts[0])
	}

	// Must be under a storage mount (e.g. /mnt, /srv, /storage) or a custom root dataset pool
	if !strings.HasPrefix(cleaned, "/mnt/") && !strings.HasPrefix(cleaned, "/srv/") && !strings.HasPrefix(cleaned, "/storage/") {
		// If directly under root, it must not be empty or forbidden
		if len(parts) == 0 || parts[0] == "" {
			return errors.New("share path cannot be root directory '/'")
		}
	}
	return nil
}

func DevicePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("device path cannot be empty")
	}
	if strings.HasPrefix(path, "-") {
		return errors.New("device path cannot start with a hyphen")
	}
	if !strings.HasPrefix(path, "/dev/") {
		// Allow bare disk names (e.g. "sda", "nvme0n1") - convert conceptually
		if regexp.MustCompile(`^[a-zA-Z0-9._-]+$`).MatchString(path) {
			return nil
		}
		return fmt.Errorf("invalid device path '%s': must start with /dev/", path)
	}
	if !devicePathRegex.MatchString(path) {
		return fmt.Errorf("device path '%s' contains forbidden characters", path)
	}
	if strings.Contains(path, "..") {
		return errors.New("device path cannot contain '..'")
	}
	return nil
}

func NfsClient(client string) error {
	client = strings.TrimSpace(client)
	if client == "" {
		return errors.New("NFS client cannot be empty")
	}
	if client == "*" {
		return nil
	}
	if strings.ContainsAny(client, "\n\r\x00 \t()") {
		return errors.New("NFS client contains forbidden characters")
	}
	if !nfsClientRegex.MatchString(client) {
		return fmt.Errorf("invalid NFS client specification '%s'", client)
	}
	return nil
}

func NfsOptions(opts string) error {
	opts = strings.TrimSpace(opts)
	if opts == "" {
		return nil
	}
	if strings.ContainsAny(opts, "\n\r\x00()") {
		return errors.New("NFS options cannot contain newlines, carriage returns, or parentheses")
	}

	parts := strings.Split(opts, ",")
	for _, opt := range parts {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		if strings.HasPrefix(opt, "anonuid=") || strings.HasPrefix(opt, "anongid=") || strings.HasPrefix(opt, "fsid=") {
			continue
		}
		if !allowedNfsOptions[opt] {
			return fmt.Errorf("unrecognized or disallowed NFS export option '%s'", opt)
		}
	}
	return nil
}

var validCompressions = map[string]bool{
	"on": true, "off": true, "lzjb": true, "lz4": true, "gzip": true,
	"gzip-1": true, "gzip-2": true, "gzip-3": true, "gzip-4": true, "gzip-5": true,
	"gzip-6": true, "gzip-7": true, "gzip-8": true, "gzip-9": true,
	"zle": true, "zstd": true, "zstd-fast": true,
}

var validRecordSizes = map[string]bool{
	"512": true, "1k": true, "2k": true, "4k": true, "8k": true, "16k": true,
	"32k": true, "64k": true, "128k": true, "256k": true, "512k": true, "1m": true,
}

func DatasetProperty(prop, value string) error {
	prop = strings.TrimSpace(strings.ToLower(prop))
	if !allowedDatasetProps[prop] {
		return fmt.Errorf("setting dataset property '%s' is not allowed or unsupported", prop)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return errors.New("property value cannot contain newline, carriage return, or null bytes")
	}

	valLower := strings.TrimSpace(strings.ToLower(value))
	switch prop {
	case "compression":
		if !validCompressions[valLower] && !strings.HasPrefix(valLower, "zstd-") {
			return fmt.Errorf("invalid compression algorithm '%s'", value)
		}
	case "recordsize":
		if !validRecordSizes[valLower] {
			return fmt.Errorf("invalid recordsize '%s': must be power of 2 between 512 and 1M", value)
		}
	case "atime":
		if valLower != "on" && valLower != "off" {
			return fmt.Errorf("invalid atime setting '%s': must be 'on' or 'off'", value)
		}
	case "xattr":
		if valLower != "on" && valLower != "off" && valLower != "sa" {
			return fmt.Errorf("invalid xattr setting '%s': must be 'on', 'off', or 'sa'", value)
		}
	case "acltype":
		if valLower != "noacl" && valLower != "posix" && valLower != "posixacl" && valLower != "nfsv4" {
			return fmt.Errorf("invalid acltype '%s'", value)
		}
	}
	return nil
}

func PoolProperty(prop, value string) error {
	prop = strings.TrimSpace(strings.ToLower(prop))
	if !allowedPoolProps[prop] {
		return fmt.Errorf("setting pool property '%s' is not allowed or unsupported", prop)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return errors.New("property value cannot contain newline, carriage return, or null bytes")
	}
	return nil
}
