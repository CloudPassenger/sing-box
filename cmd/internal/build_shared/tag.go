package build_shared

import (
	"strings"

	"github.com/sagernet/sing-box/common/badversion"
	"github.com/sagernet/sing/common"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/shell"
)

func ReadTag() (string, error) {
	currentTag, err := shell.Exec("git", "describe", "--tags").ReadOutput()
	if err != nil {
		return currentTag, err
	}
	currentTagRev, _ := shell.Exec("git", "describe", "--tags", "--abbrev=0").ReadOutput()
	if currentTagRev == currentTag {
		return strings.TrimPrefix(currentTag, "v"), nil
	}
	shortCommit, _ := shell.Exec("git", "rev-parse", "--short", "HEAD").ReadOutput()
	if upstreamVersion, forkVersion, isForkTag := splitForkTag(currentTagRev); isForkTag {
		return upstreamVersion + "-superpower-" + forkVersion + "-" + shortCommit, nil
	}
	if trackSuffix := detectForkTrackSuffix(); trackSuffix != "" {
		version := badversion.Parse(strings.TrimPrefix(currentTagRev, "v"))
		return version.String() + "-" + trackSuffix + "-" + shortCommit, nil
	}
	version := badversion.Parse(strings.TrimPrefix(currentTagRev, "v"))
	return version.String() + "-" + shortCommit, nil
}

func ReadTagVersionRev() (badversion.Version, error) {
	currentTagRev := common.Must1(shell.Exec("git", "describe", "--tags", "--abbrev=0").ReadOutput())
	if upstreamVersion, _, isForkTag := splitForkTag(currentTagRev); isForkTag {
		return badversion.Parse(upstreamVersion), nil
	}
	return badversion.Parse(strings.TrimPrefix(currentTagRev, "v")), nil
}

func ReadTagVersion() (badversion.Version, error) {
	currentTag := common.Must1(shell.Exec("git", "describe", "--tags").ReadOutput())
	currentTagRev := common.Must1(shell.Exec("git", "describe", "--tags", "--abbrev=0").ReadOutput())
	version := common.Must1(ReadTagVersionRev())
	if currentTagRev != currentTag {
		if version.PreReleaseIdentifier == "" {
			version.Patch++
		}
	}
	return version, nil
}

func TestFlightVersion(version badversion.Version) string {
	return F.ToString(version.Major, ".", version.Minor, ".10")
}

func splitForkTag(tag string) (string, string, bool) {
	tag = strings.TrimPrefix(tag, "v")
	const forkSeparator = "-superpower-"
	separatorIndex := strings.Index(tag, forkSeparator)
	if separatorIndex < 0 {
		return "", "", false
	}
	upstreamVersion := tag[:separatorIndex]
	forkVersion := tag[separatorIndex+len(forkSeparator):]
	if upstreamVersion == "" || forkVersion == "" {
		return "", "", false
	}
	return upstreamVersion, forkVersion, true
}

func detectForkTrackSuffix() string {
	branchName, err := shell.Exec("git", "branch", "--show-current").ReadOutput()
	if err != nil {
		return ""
	}
	return detectForkTrackSuffixFromBranch(branchName)
}

func detectForkTrackSuffixFromBranch(branchName string) string {
	switch branchName {
	case "superpower", "superpower-testing":
		return branchName
	default:
		return ""
	}
}
