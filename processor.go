package main

import (
	"encoding/json"
	"fmt"
	"github.com/davidbyttow/govips/v2/vips"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ImageData struct {
	FullPath    string `json:"full_path"`
	ThumbPath   string `json:"thumb_path"`
	DisplayPath string `json:"display_path"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Tiles       string `json:"tiles,omitempty"`
}

var logger = log.Default()

const thumbnailHeight = 400
const slideHeight = 2000
const tileMinDimension = 4100

func main() {
	skipFileNames := []string{".DS_Store", "thumbnail", "display", "html", "dzi", "json", "xml"}
	//imageFileSuffixes := []string{".png", ".jpg", ".jpeg"}
	//stripDimsRegex := regexp.MustCompile(`-\d{4,5} x \d{4,5}`)

	var currentDir string
	var err error
	var imageDataMap = map[string]map[string]*ImageData{}

	vips.Startup(nil)
	vips.LoggingSettings(nil, vips.LogLevelMessage)
	defer vips.Shutdown()

	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		for _, skipFileName := range skipFileNames {
			// don't process non-images or already generated images
			if strings.Contains(d.Name(), skipFileName) {
				return nil
			}
		}
		logger.Println(path)

		if d.IsDir() {
			if d.Name() == "." {
				return nil
			}

			// skip dz tiles generated externally or previously
			if d.IsDir() && strings.HasSuffix(d.Name(), "_files") {
				return filepath.SkipDir
			}

			// new directory, create image data sub map
			if currentDir != path {
				imageDataMap[path] = map[string]*ImageData{}
			}
			currentDir = path

			// nothing else to do with directories
			return nil
		} else {
			ext := filepath.Ext(d.Name())
			imageName := strings.TrimSuffix(d.Name(), ext)

			image, err := vips.NewImageFromFile(path)
			defer image.Close()

			if err != nil {
				panic(err)
			}

			var imageData ImageData

			// png is nice but way too big
			if ext == ".png" {
				logger.Printf("Retyping image to jpg: %s", imageName)

				ext = ".jpg"
				// vips image to jpg
				imageFile := fmt.Sprintf("%s%s", imageName, ext)
				path := filepath.Join(currentDir, imageFile)

				// for web viewing/consistency with generated tiles
				err := image.ToColorSpace(vips.InterpretationSRGB)
				if err != nil {
					panic(err)
				}

				jpgImageBytes, _, err := image.ExportJpeg(vips.NewJpegExportParams())
				if err != nil {
					panic(err)
				}

				err = os.WriteFile(path, jpgImageBytes, 0644)
				if err != nil {
					panic(err)
				}
			}

			thumbnailPath := filepath.Join(currentDir, imageName+"-thumbnail"+ext)
			displayPath := filepath.Join(currentDir, imageName+"-display"+ext)
			fullPath := filepath.Join(currentDir, imageName+ext)

			imageData.DisplayPath = displayPath
			imageData.FullPath = fullPath
			imageData.ThumbPath = thumbnailPath
			imageData.Height = image.Height()
			imageData.Width = image.Width()

			// all appropriate images should be jpg, skip anything else
			if ext == ".jpg" {
				// the grid thumbnail
				thumbnail, err := vips.NewThumbnailFromFile(fullPath, math.MaxInt16, thumbnailHeight, vips.InterestingNone)
				if err != nil {
					panic(err)
				}
				thumbnailBytes, _, err := thumbnail.ExportNative()
				if err != nil {
					panic(err)
				}
				err = os.WriteFile(thumbnailPath, thumbnailBytes, 0644)
				if err != nil {
					panic(err)
				}

				// the slide image
				if image.Width() > slideHeight || image.Height() > slideHeight {
					display, err := vips.NewThumbnailFromFile(fullPath, math.MaxInt16, slideHeight, vips.InterestingNone)
					if err != nil {
						panic(err)
					}

					displayBytes, _, err := display.ExportNative()
					if err != nil {
						panic(err)
					}

					err = os.WriteFile(displayPath, displayBytes, 0644)
					if err != nil {
						panic(err)
					}
				}

				// generate tiles if necessary
				if image.Width() > tileMinDimension || image.Height() > tileMinDimension {
					logger.Printf("Generating tiles for %s", imageName)

					// Shell out because govips doesn't have a dzsave binding
					vipsDzCmd := exec.Command("vips", "dzsave", fullPath, filepath.Join(currentDir, imageName), "--depth", "onetile")
					err := vipsDzCmd.Run()
					if err != nil {
						panic(err)
					}

					imageData.Tiles = filepath.Join(currentDir, imageName+"_files")

					// delete the unnecessary generated meta files
					err = os.Remove(filepath.Join(currentDir, imageName+".dzi"))
					if err != nil {
						logger.Println(err)
					}
				}
			}

			imageDataMap[currentDir][imageName] = &imageData
		}

		return nil
	})

	if err != nil {
		fmt.Println(err)
		return
	}

	for dir, imageData := range imageDataMap {
		writeDirImageData(dir, imageData)
	}
}

func writeDirImageData(dir string, imageData map[string]*ImageData) {
	logger.Printf("Writing JSON to %s/images.json", dir)

	logger.Printf("Opening JSON file %s", dir)
	jsonFile, err := os.Create(filepath.Join(dir, "images.json"))

	defer func() {
		logger.Printf("Closing JSON file for %s", dir)
		err := jsonFile.Close()
		if err != nil {
			logger.Println(err)
			return
		}
	}()

	imageJson, err := json.MarshalIndent(imageData, "", "  ")
	if err != nil {
		panic(err)
	}
	_, err = jsonFile.Write(imageJson)
	if err != nil {
		panic(err)
	}
}
