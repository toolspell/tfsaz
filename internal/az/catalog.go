package az

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type resourceLocation struct {
	Area            string      `json:"area"`
	ResourceName    string      `json:"resourceName"`
	RouteTemplate   string      `json:"routeTemplate"`
	MinVersion      json.Number `json:"minVersion"`
	MaxVersion      json.Number `json:"maxVersion"`
	ReleasedVersion json.Number `json:"releasedVersion"`
	ResourceVersion int         `json:"resourceVersion"`
	raw             json.RawMessage
}

func fetchCatalog(ctx context.Context, c *Client) ([]resourceLocation, error) {
	query := url.Values{"allHostTypes": {"true"}}
	resp, err := c.request(ctx, http.MethodOptions, []string{"_apis"}, query, "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := readResponse(resp.Body, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Value []json.RawMessage `json:"value"`
	}
	if json.Unmarshal(body, &catalog) != nil || catalog.Value == nil {
		return nil, errors.New("Azure DevOps returned an invalid resource catalog")
	}
	locations := make([]resourceLocation, len(catalog.Value))
	for i, raw := range catalog.Value {
		location := &locations[i]
		if json.Unmarshal(raw, location) != nil || location.Area == "" || location.ResourceName == "" {
			return nil, errors.New("Azure DevOps returned an invalid resource catalog entry")
		}
		location.raw = raw
	}
	return locations, nil
}

var (
	catalogVersionPattern = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(-preview(?:\.([0-9]+))?)?$`)
	catalogSlotPattern    = regexp.MustCompile(`^([a-zA-Z0-9_.$@~-]*)\{(\*?)([a-zA-Z_][a-zA-Z0-9_]*)(\??)\}([a-zA-Z0-9_.$@~-]*)$`)
	catalogLiteralPattern = regexp.MustCompile(`^[a-zA-Z0-9_.~$-]+$`)
	catalogPercentEscape  = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)
)

type catalogVersion struct {
	major, minor int
}

func compareCatalogVersions(a, b catalogVersion) int {
	if a.major != b.major {
		return a.major - b.major
	}
	return a.minor - b.minor
}

func catalogVersionNumbers(major, minor string) (catalogVersion, error) {
	if minor == "" {
		minor = "0"
	}
	majorNumber, majorErr := strconv.ParseUint(major, 10, 31)
	minorNumber, minorErr := strconv.ParseUint(minor, 10, 31)
	return catalogVersion{int(majorNumber), int(minorNumber)}, errors.Join(majorErr, minorErr)
}

func catalogVersionBound(raw json.Number) (catalogVersion, bool) {
	major, minor, _ := strings.Cut(string(raw), ".")
	version, err := catalogVersionNumbers(major, minor)
	return version, err == nil
}

func parseAPIVersion(version string) (catalogVersion, string, int, bool) {
	match := catalogVersionPattern.FindStringSubmatch(version)
	if match == nil {
		return catalogVersion{}, "", 0, false
	}
	number, err := catalogVersionNumbers(match[1], match[2])
	if err != nil {
		return catalogVersion{}, "", 0, false
	}
	if match[4] == "" {
		return number, "", 0, true
	}
	revision, err := strconv.ParseUint(match[4], 10, 31)
	return number, match[4], int(revision), err == nil
}

func validAPIVersion(version string) bool {
	_, _, _, valid := parseAPIVersion(version)
	return valid
}

func resolveResource(locations []resourceLocation, area, resource, version string, route map[string]string) ([]string, string, error) {
	if version == "" {
		version = "5.0"
	}
	requested, previewSuffix, revision, valid := parseAPIVersion(version)
	if !valid {
		return nil, "", errors.New("invalid devops invoke API version")
	}
	parameters := make(map[string]string, len(route))
	for key, value := range route {
		key = strings.ToLower(key)
		if key == "area" || key == "resource" {
			return nil, "", errors.New("devops invoke route parameters cannot override area or resource")
		}
		if _, exists := parameters[key]; exists {
			return nil, "", errors.New("duplicate devops invoke route parameter")
		}
		parameters[key] = value
	}

	var selected []string
	var selectedVersion string
	var selectedMax catalogVersion
	var selectedOmitted int
	var found, compatible, ambiguous bool
	for _, location := range locations {
		if !strings.EqualFold(area, location.Area) || !strings.EqualFold(resource, location.ResourceName) {
			continue
		}
		found = true
		minVersion, minOK := catalogVersionBound(location.MinVersion)
		maxVersion, maxOK := catalogVersionBound(location.MaxVersion)
		releasedVersion, releasedOK := catalogVersionBound(location.ReleasedVersion)
		if !minOK || !maxOK || !releasedOK || location.ResourceVersion < 0 ||
			compareCatalogVersions(minVersion, maxVersion) > 0 ||
			compareCatalogVersions(releasedVersion, maxVersion) > 0 ||
			compareCatalogVersions(requested, minVersion) < 0 {
			continue
		}
		compatible = true
		path, omitted, ok := bindResourceRoute(location, parameters)
		if !ok {
			continue
		}
		negotiated := version
		if compareCatalogVersions(requested, maxVersion) > 0 {
			negotiated = strconv.Itoa(maxVersion.major) + "." + strconv.Itoa(maxVersion.minor)
			if compareCatalogVersions(releasedVersion, maxVersion) < 0 {
				negotiated += "-preview"
			}
		} else if revision > location.ResourceVersion {
			negotiated = strings.TrimSuffix(version, previewSuffix) + strconv.Itoa(location.ResourceVersion)
		}

		comparison := compareCatalogVersions(maxVersion, selectedMax)
		if selected == nil || comparison > 0 || (comparison == 0 && omitted < selectedOmitted) {
			selected, selectedVersion, selectedMax, selectedOmitted = path, negotiated, maxVersion, omitted
			ambiguous = false
		} else if comparison == 0 && omitted == selectedOmitted &&
			(!slices.Equal(selected, path) || selectedVersion != negotiated) {
			ambiguous = true
		}
	}
	switch {
	case ambiguous:
		return nil, "", errors.New("devops invoke resource matches multiple collection routes")
	case selected != nil:
		return selected, selectedVersion, nil
	case !found:
		return nil, "", errors.New("devops invoke resource is absent from the collection catalog")
	case !compatible:
		return nil, "", errors.New("devops invoke API version is unavailable for this resource")
	default:
		return nil, "", errors.New("devops invoke route parameters do not match a safe collection route")
	}
}

func bindResourceRoute(location resourceLocation, parameters map[string]string) ([]string, int, bool) {
	template := strings.Split(strings.TrimSuffix(strings.TrimPrefix(location.RouteTemplate, "/"), "/"), "/")
	apiIndex := -1
	lastFixed := -1
	for i, part := range template {
		if collectionAPIRoot(part) {
			if apiIndex >= 0 {
				return nil, 0, false
			}
			apiIndex = i
		}
		match := catalogSlotPattern.FindStringSubmatch(part)
		if match == nil || match[1] != "" || match[5] != "" || strings.EqualFold(match[3], "area") || strings.EqualFold(match[3], "resource") {
			lastFixed = i
		}
	}
	if apiIndex < 0 || apiIndex == len(template)-1 {
		return nil, 0, false
	}

	path := make([]string, 0, len(template))
	used := make(map[string]bool, len(parameters))
	omitted := 0
	prefixOmitted := false
	tailOmitted := false
	for i, part := range template {
		match := catalogSlotPattern.FindStringSubmatch(part)
		if match == nil {
			if !catalogLiteralPattern.MatchString(part) || (i != apiIndex && !validCatalogRouteValue(part)) || (i < apiIndex && prefixOmitted) {
				return nil, 0, false
			}
			path = append(path, part)
			continue
		}
		prefix, star, key, optional := match[1], match[2] == "*", strings.ToLower(match[3]), match[4] == "?"
		if i == apiIndex+1 && strings.EqualFold(template[apiIndex], "_apis") && key != "area" && key != "resource" {
			return nil, 0, false
		}
		suffix := match[5]
		if star && (i != len(template)-1 || prefix != "" || suffix != "" || key == "area" || key == "resource") {
			return nil, 0, false
		}
		value, present := parameters[key]
		switch key {
		case "area":
			value, present = location.Area, true
		case "resource":
			value, present = location.ResourceName, true
		default:
			used[key] = present
		}
		if !present {
			// The SDK omits unused whole slots. Preserve the optional project/team
			// prefix and trailing IDs without dropping IDs between fixed segments.
			if prefix != "" || suffix != "" || (!optional && i > apiIndex && i < lastFixed) {
				return nil, 0, false
			}
			prefixOmitted = prefixOmitted || i < apiIndex
			tailOmitted = tailOmitted || (!optional && i > apiIndex)
			omitted++
			continue
		}
		if (i < apiIndex && prefixOmitted) || tailOmitted {
			return nil, 0, false
		}
		parts := []string{value}
		if star {
			parts = strings.Split(strings.TrimPrefix(value, "/"), "/")
		}
		for _, segment := range parts {
			if !validCatalogRouteValue(segment) || !validCatalogRouteValue(prefix+segment+suffix) {
				return nil, 0, false
			}
			path = append(path, prefix+segment+suffix)
		}
	}
	// Reject paths ending at /_apis and every parameter the template didn't bind.
	if len(path) == 0 || collectionAPIRoot(path[len(path)-1]) {
		return nil, 0, false
	}
	for key := range parameters {
		if !used[key] {
			return nil, 0, false
		}
	}
	return path, omitted, true
}

func validCatalogRouteValue(value string) bool {
	if !utf8.ValidString(value) || !validSegment(value) {
		return false
	}
	for {
		// IIS can normalize trailing dots/spaces and intermediaries can strip
		// semicolon parameters. Prevent either from creating path control segments.
		normalized := strings.TrimRight(strings.TrimSpace(value), ". ")
		if normalized == "" || collectionAPIRoot(normalized) ||
			strings.ContainsRune(normalized, ';') {
			return false
		}
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return !catalogPercentEscape.MatchString(value)
		}
		if decoded == value {
			return true
		}
		value = decoded
	}
}
