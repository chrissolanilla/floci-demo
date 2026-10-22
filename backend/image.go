
package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

//5mb
const maxImageSize = 5 * 1024 * 1024
const maxFormSize = maxImageSize + 64 * 1024
const maxImagePixels = 40_000_000

func imageKey(userID int64) string {
	return fmt.Sprintf("users/%d/image", userID)
}

func (a *app) uploadImage(w http.ResponseWriter, r *http.Request) {
	user, ok := a.currentUser(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormSize)
	err := r.ParseMultipartForm(maxFormSize)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, http.StatusRequestEntityTooLarge, "image must be 5 MiB or smaller")
			return
		}

		fail(w, http.StatusBadRequest, "invalid image upload")
		return
	}

	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["image"]
	if len(r.MultipartForm.File) != 1 || len(files) != 1 {
		fail(w, http.StatusBadRequest, "choose exactly one image")
		return
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		fail(w, http.StatusBadRequest, "could not open image")
		return
	}

	defer file.Close()
	//read the image into memory(maybe we want to get it in packets for scale but just a demo)
	data, err := io.ReadAll(io.LimitReader(file, maxImageSize+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "could not read image")
		return
	}

	if len(data) > maxImageSize {
		fail(w, http.StatusRequestEntityTooLarge, "image must be 5 MiB or smaller")
		return
	}

	//check the image format and dimensions
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid image format")
		return
	}

	if config.Width <= 0 || config.Height <= 0 {
		fail(w, http.StatusBadRequest, "invalid image dimensions")
		return
	}

	pixels := int64(config.Width) * int64(config.Height)
	if pixels > maxImagePixels {
		fail(w, http.StatusBadRequest, "image has too many pixels")
		return
	}

	_, _, err = image.Decode(bytes.NewReader(data))
	if err != nil {
		fail(w, http.StatusBadRequest, "image is damaged or incomplete")
		return
	}

	//upload the image to S3
	key := imageKey(user.ID)
	contentType := "image/" + format

	_, err = a.s3.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(a.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})

	if err != nil {
		serverError(w, err)
		return
	}

	// mark the user as having an image
	_, err = a.db.ExecContext(
		r.Context(),
		"UPDATE users SET has_image = TRUE WHERE id = $1",
		user.ID,
	)

	if err != nil {
		serverError(w, err)
		return
	}

	log.Printf("user %d uploaded %d bytes to S3", user.ID, len(data))

	user.HasImage = true
	jsonResponse(w, http.StatusOK, user)
}

func (a *app) getImage(w http.ResponseWriter, r *http.Request) {
	user, ok := a.currentUser(w, r)
	if !ok {
		return
	}

	if !user.HasImage {
		fail(w, http.StatusNotFound, "upload an image first")
		return
	}

	key := imageKey(user.ID)
	object, err := a.s3.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		serverError(w, err)
		return
	}

	defer object.Body.Close()
	//send the image back to the browser
	contentType := aws.ToString(object.ContentType)
	w.Header().Set("Content-Type", contentType)
	_, err = io.Copy(w, object.Body)
	if err != nil {
		log.Printf("serve image: %v", err)
	}
}
