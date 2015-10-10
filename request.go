/*
 * Minio Go Library for Amazon S3 Legacy v2 Signature Compatible Cloud Storage (C) 2015 Minio, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package minio

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// operation - rest operation
type operation struct {
	HTTPServer string
	HTTPMethod string
	HTTPPath   string
}

// request - a http request
type request struct {
	req     *http.Request
	config  *Config
	body    io.ReadSeeker
	expires int64
}

// getURLEncodedPath encode the strings from UTF-8 byte representations to HTML hex escape sequences
//
// This is necessary since regular url.Parse() and url.Encode() functions do not support UTF-8
// non english characters cannot be parsed due to the nature in which url.Encode() is written
//
// This function on the other hand is a direct replacement for url.Encode() technique to support
// pretty much every UTF-8 character.
func getURLEncodedPath(pathName string) string {
	// if object matches reserved string, no need to encode them
	reservedNames := regexp.MustCompile("^[a-zA-Z0-9-_.~/]+$")
	if reservedNames.MatchString(pathName) {
		return pathName
	}
	var encodedPathname string
	for _, s := range pathName {
		if 'A' <= s && s <= 'Z' || 'a' <= s && s <= 'z' || '0' <= s && s <= '9' { // §2.3 Unreserved characters (mark)
			encodedPathname = encodedPathname + string(s)
			continue
		}
		switch s {
		case '-', '_', '.', '~', '/': // §2.3 Unreserved characters (mark)
			encodedPathname = encodedPathname + string(s)
			continue
		default:
			len := utf8.RuneLen(s)
			if len < 0 {
				// if utf8 cannot convert return the same string as is
				return pathName
			}
			u := make([]byte, len)
			utf8.EncodeRune(u, s)
			for _, r := range u {
				hex := hex.EncodeToString([]byte{r})
				encodedPathname = encodedPathname + "%" + strings.ToUpper(hex)
			}
		}
	}
	return encodedPathname
}

func path2BucketAndObject(path string) (bucketName, objectName string) {
	pathSplits := strings.SplitN(path, "?", 2)
	splits := strings.SplitN(pathSplits[0], separator, 3)
	switch len(splits) {
	case 0, 1:
		bucketName = ""
		objectName = ""
	case 2:
		bucketName = splits[1]
		objectName = ""
	case 3:
		bucketName = splits[1]
		objectName = splits[2]
	}
	return bucketName, objectName
}

// path2Object gives objectName from URL path
func path2Object(path string) (objectName string) {
	_, objectName = path2BucketAndObject(path)
	return
}

// path2Bucket gives bucketName from URL path
func path2Bucket(path string) (bucketName string) {
	bucketName, _ = path2BucketAndObject(path)
	return
}

// path2Query gives query part from URL path
func path2Query(path string) (query string) {
	pathSplits := strings.SplitN(path, "?", 2)
	if len(pathSplits) > 1 {
		query = pathSplits[1]
	}
	return
}

func (op *operation) getRequestURL(config Config) (url string) {
	// parse URL for the combination of HTTPServer + HTTPPath
	url = op.HTTPServer + separator
	if !config.isVirtualStyle {
		url += path2Bucket(op.HTTPPath)
	}
	objectName := getURLEncodedPath(path2Object(op.HTTPPath))
	queryPath := path2Query(op.HTTPPath)
	if objectName == "" && queryPath != "" {
		url += "?" + queryPath
		return
	}
	if objectName != "" && queryPath == "" {
		url += separator + objectName
		return
	}
	if objectName != "" && queryPath != "" {
		url += separator + objectName + "?" + queryPath
	}
	return
}

func newPresignedRequest(op *operation, config *Config, expires int64) (*request, error) {
	// if no method default to POST
	method := op.HTTPMethod
	if method == "" {
		method = "POST"
	}

	u := op.getRequestURL(*config)

	// get a new HTTP request, for the requested method
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}

	// set UserAgent
	req.Header.Set("User-Agent", config.userAgent)

	// set Accept header for response encoding style, if available
	if config.AcceptType != "" {
		req.Header.Set("Accept", config.AcceptType)
	}

	// save for subsequent use
	r := new(request)
	r.config = config
	r.expires = expires
	r.req = req
	r.body = nil

	return r, nil
}

// newUnauthenticatedRequest - instantiate a new unauthenticated request
func newUnauthenticatedRequest(op *operation, config *Config, body io.Reader) (*request, error) {
	// if no method default to POST
	method := op.HTTPMethod
	if method == "" {
		method = "POST"
	}

	u := op.getRequestURL(*config)

	// get a new HTTP request, for the requested method
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}

	// set UserAgent
	req.Header.Set("User-Agent", config.userAgent)

	// set Accept header for response encoding style, if available
	if config.AcceptType != "" {
		req.Header.Set("Accept", config.AcceptType)
	}

	// add body
	switch {
	case body == nil:
		req.Body = nil
	default:
		req.Body = ioutil.NopCloser(body)
	}

	// save for subsequent use
	r := new(request)
	r.req = req
	r.config = config

	return r, nil
}

// newRequest - instantiate a new request
func newRequest(op *operation, config *Config, body io.ReadSeeker) (*request, error) {
	// if no method default to POST
	method := op.HTTPMethod
	if method == "" {
		method = "POST"
	}

	u := op.getRequestURL(*config)

	// get a new HTTP request, for the requested method
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}

	// set UserAgent
	req.Header.Set("User-Agent", config.userAgent)

	// set Accept header for response encoding style, if available
	if config.AcceptType != "" {
		req.Header.Set("Accept", config.AcceptType)
	}

	// add body
	switch {
	case body == nil:
		req.Body = nil
	default:
		req.Body = ioutil.NopCloser(body)
	}

	// save for subsequent use
	r := new(request)
	r.config = config
	r.req = req
	r.body = body

	return r, nil
}

// Do - start the request
func (r *request) Do() (resp *http.Response, err error) {
	if r.config.AccessKeyID != "" && r.config.SecretAccessKey != "" {
		r.SignV2()
	}
	transport := http.DefaultTransport
	if r.config.Transport != nil {
		transport = r.config.Transport
	}
	// do not use http.Client{}, while it may seem intuitive but the problem seems to be
	// that http.Client{} internally follows redirects and there is no easier way to disable
	// it from outside using a configuration parameter -
	//     this auto redirect causes complications in verifying subsequent errors
	//
	// The best is to use RoundTrip() directly, so the request comes back to the caller where
	// we are going to handle such replies. And indeed that is the right thing to do here.
	//
	return transport.RoundTrip(r.req)
}

// Set - set additional headers if any
func (r *request) Set(key, value string) {
	r.req.Header.Set(key, value)
}

// Get - get header values
func (r *request) Get(key string) string {
	return r.req.Header.Get(key)
}

// https://${S3_BUCKET}.s3.amazonaws.com/${S3_OBJECT}?AWSAccessKeyId=${S3_ACCESS_KEY}&Expires=${TIMESTAMP}&Signature=${SIGNATURE}
func (r *request) PreSignV2() (string, error) {
	if r.config.AccessKeyID == "" || r.config.SecretAccessKey == "" {
		return "", errors.New("presign requires accesskey and secretkey")
	}
	// Add date if not present
	d := time.Now().UTC()
	if date := r.Get("Date"); date == "" {
		r.Set("Date", d.Format(http.TimeFormat))
	}
	epochExpires := d.Unix() + r.expires
	signText := fmt.Sprintf("%s\n\n\n%d\n%s", r.req.Method, epochExpires, r.req.URL.Path)
	hm := hmac.New(sha1.New, []byte(r.config.SecretAccessKey))
	hm.Write([]byte(signText))

	query := r.req.URL.Query()
	query.Set("AWSAccessKeyId", r.config.AccessKeyID)
	query.Set("Expires", strconv.FormatInt(epochExpires, 10))
	query.Set("Signature", base64.StdEncoding.EncodeToString(hm.Sum(nil)))
	r.req.URL.RawQuery = query.Encode()

	return r.req.URL.String(), nil
}

func (r *request) PostPresignSignature(policyBase64 string) string {
	hm := hmac.New(sha1.New, []byte(r.config.SecretAccessKey))
	hm.Write([]byte(policyBase64))

	signature := base64.StdEncoding.EncodeToString(hm.Sum(nil))
	return signature
}

// Authorization = "AWS" + " " + AWSAccessKeyId + ":" + Signature;
// Signature = Base64( HMAC-SHA1( YourSecretAccessKeyID, UTF-8-Encoding-Of( StringToSign ) ) );
//
// StringToSign = HTTP-Verb + "\n" +
//  	Content-MD5 + "\n" +
//  	Content-Type + "\n" +
//  	Date + "\n" +
//  	CanonicalizedAmzHeaders +
//  	CanonicalizedResource;
//
// CanonicalizedResource = [ "/" + Bucket ] +
//  	<HTTP-Request-URI, from the protocol name up to the query string> +
//  	[ subresource, if present. For example "?acl", "?location", "?logging", or "?torrent"];
//
// CanonicalizedAmzHeaders = <described below>

// SignV2 the request before Do() (version 2.0)
func (r *request) SignV2() {
	// Add date if not present
	if date := r.Get("Date"); date == "" {
		r.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	}
	// Calculate HMAC for secretAccessKey
	hm := hmac.New(sha1.New, []byte(r.config.SecretAccessKey))
	hm.Write([]byte(r.getStringToSign()))

	// prepare auth header
	authHeader := new(bytes.Buffer)
	authHeader.WriteString(fmt.Sprintf("AWS %s:", r.config.AccessKeyID))
	encoder := base64.NewEncoder(base64.StdEncoding, authHeader)
	encoder.Write(hm.Sum(nil))
	encoder.Close()

	// Set Authorization header
	r.req.Header.Set("Authorization", authHeader.String())
}

// From the Amazon docs:
//
// StringToSign = HTTP-Verb + "\n" +
// 	 Content-MD5 + "\n" +
//	 Content-Type + "\n" +
//	 Date + "\n" +
//	 CanonicalizedAmzHeaders +
//	 CanonicalizedResource;
func (r *request) getStringToSign() string {
	buf := new(bytes.Buffer)
	// write standard headers
	r.writeDefaultHeaders(buf)
	// write canonicalized AMZ headers if any
	r.writeCanonicalizedAmzHeaders(buf)
	// write canonicalized Query resources if any
	r.writeCanonicalizedResource(buf)
	return buf.String()
}

func (r *request) writeDefaultHeaders(buf *bytes.Buffer) {
	buf.WriteString(r.req.Method)
	buf.WriteByte('\n')
	buf.WriteString(r.req.Header.Get("Content-MD5"))
	buf.WriteByte('\n')
	buf.WriteString(r.req.Header.Get("Content-Type"))
	buf.WriteByte('\n')
	buf.WriteString(r.req.Header.Get("Date"))
	buf.WriteByte('\n')
}

func (r *request) writeCanonicalizedAmzHeaders(buf *bytes.Buffer) {
	var amzHeaders []string
	vals := make(map[string][]string)
	for k, vv := range r.req.Header {
		// all the AMZ headers go lower
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz") {
			amzHeaders = append(amzHeaders, lk)
			vals[lk] = vv
		}
	}
	sort.Strings(amzHeaders)
	for _, k := range amzHeaders {
		buf.WriteString(k)
		buf.WriteByte(':')
		for idx, v := range vals[k] {
			if idx > 0 {
				buf.WriteByte(',')
			}
			if strings.Contains(v, "\n") {
				// TODO: "Unfold" long headers that
				// span multiple lines (as allowed by
				// RFC 2616, section 4.2) by replacing
				// the folding white-space (including
				// new-line) by a single space.
				buf.WriteString(v)
			} else {
				buf.WriteString(v)
			}
		}
		buf.WriteByte('\n')
	}
}

// Must be sorted:
var resourceList = []string{
	"acl",
	"location",
	"logging",
	"notification",
	"partNumber",
	"policy",
	"response-content-type",
	"response-content-language",
	"response-expires",
	"response-cache-control",
	"response-content-disposition",
	"response-content-encoding",
	"requestPayment",
	"torrent",
	"uploadId",
	"uploads",
	"versionId",
	"versioning",
	"versions",
	"website",
}

// From the Amazon docs:
//
// CanonicalizedResource = [ "/" + Bucket ] +
// 	  <HTTP-Request-URI, from the protocol name up to the query string> +
// 	  [ sub-resource, if present. For example "?acl", "?location", "?logging", or "?torrent"];
func (r *request) writeCanonicalizedResource(buf *bytes.Buffer) error {
	requestURL := r.req.URL
	if r.config.isVirtualStyle {
		for k, v := range regions {
			if v == r.config.Region {
				path := "/" + strings.TrimSuffix(requestURL.Host, "."+k)
				path += requestURL.Path
				buf.WriteString(getURLEncodedPath(path))
				break
			}
		}
	} else {
		buf.WriteString(getURLEncodedPath(requestURL.Path))
	}
	sort.Strings(resourceList)
	if requestURL.RawQuery != "" {
		var n int
		vals, _ := url.ParseQuery(requestURL.RawQuery)
		// loop through all the supported resourceList
		for _, resource := range resourceList {
			if vv, ok := vals[resource]; ok && len(vv) > 0 {
				n++
				// first element
				switch n {
				case 1:
					buf.WriteByte('?')
				// the rest
				default:
					buf.WriteByte('&')
				}
				buf.WriteString(resource)
				// request parameters
				if len(vv[0]) > 0 {
					buf.WriteByte('=')
					buf.WriteString(url.QueryEscape(vv[0]))
				}
			}
		}
	}
	return nil
}
