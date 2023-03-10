package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/opencontainers/go-digest"
	"kubegems.io/modelx/pkg/errors"
)

var GlobalExtensions = map[string]Extension{
	"idoe":  &IdoeExt{},
	"idoes": &IdoeExt{},
}

type Extension interface {
	Download(ctx context.Context, location *url.URL, into io.Writer) error
	Upload(ctx context.Context, location *url.URL, blob DescriptorWithContent) error
}

func ExtDownload(ctx context.Context, location *url.URL, into io.Writer) error {
	if ext, ok := GlobalExtensions[location.Scheme]; ok {
		return ext.Download(ctx, location, into)
	}
	return HTTPDownload(ctx, location, into)
}

func ExtUpload(ctx context.Context, location *url.URL, blob DescriptorWithContent) error {
	if ext, ok := GlobalExtensions[location.Scheme]; ok {
		return ext.Upload(ctx, location, blob)
	}
	return HTTPUpload(ctx, location, blob)
}

type BlobContent struct {
	Content       io.ReadCloser
	ContentLength int64
}

func (t *RegistryClient) HeadBlob(ctx context.Context, repository string, digest digest.Digest) (bool, error) {
	path := "/" + repository + "/blobs/" + digest.String()
	resp, err := t.request(ctx, "HEAD", path, nil, "", nil, nil)
	if err != nil {
		return false, err
	}
	return resp.StatusCode == http.StatusOK, nil
}

func (t *RegistryClient) GetBlobContent(ctx context.Context, repository string, digest digest.Digest, into io.Writer) error {
	path := "/" + repository + "/blobs/" + digest.String()
	headers := map[string][]string{}
	resp, err := t.extrequest(ctx, "GET", path, headers, 0, nil)
	if err != nil {
		return err
	}
	if http.StatusMultipleChoices <= resp.StatusCode && resp.StatusCode < http.StatusBadRequest {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return errors.NewInternalError(fmt.Errorf("no Location header found in a %s response", resp.Status))
		}
		locau, err := url.Parse(loc)
		if err != nil {
			return errors.NewInternalError(fmt.Errorf("invalid location %s: %w", loc, err))
		}
		return ExtDownload(ctx, locau, into)
	}
	_, err = io.CopyN(into, resp.Body, resp.ContentLength)
	return err
}

func (t *RegistryClient) UploadBlobContent(ctx context.Context, repository string, blob DescriptorWithContent) error {
	header := map[string][]string{
		"Content-Type": {"application/octet-stream"},
	}
	path := "/" + repository + "/blobs/" + blob.Digest.String()

	content, err := blob.GetContent()
	if err != nil {
		return err
	}

	resp, err := t.extrequest(ctx, "PUT", path, header, blob.Size, content)
	if err != nil {
		return err
	}
	if http.StatusMultipleChoices <= resp.StatusCode && resp.StatusCode < http.StatusBadRequest {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return errors.NewInternalError(fmt.Errorf("no Location header found in a %s response", resp.Status))
		}
		locau, err := url.Parse(loc)
		if err != nil {
			return errors.NewInternalError(fmt.Errorf("invalid location %s: %w", loc, err))
		}
		return ExtUpload(ctx, locau, blob)
	}
	return nil
}

func (t *RegistryClient) extrequest(ctx context.Context, method, url string, header map[string][]string, contentlen int64, content io.ReadCloser) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.Registry+url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", t.Authorization)
	req.Header.Set("User-Agent", UserAgent)
	req.Body = content
	req.ContentLength = contentlen

	norediretccli := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // do not follow redirect
		},
	}

	resp, err := norediretccli.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest && req.Method != "HEAD" {
		var apierr errors.ErrorInfo
		if resp.Header.Get("Content-Type") == "application/json" {
			if err := json.NewDecoder(resp.Body).Decode(&apierr); err != nil {
				return nil, err
			}
		} else {
			bodystr, _ := io.ReadAll(resp.Body)
			apierr.Message = string(bodystr)
		}
		apierr.HttpStatus = resp.StatusCode
		return nil, apierr
	}
	return resp, nil
}
