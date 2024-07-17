package main

import (
	"encoding/json"
	"flag"
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
	MaxWidth    int    `json:"max_width,omitempty"`
	MaxHeight   int    `json:"max_height,omitempty"`
}

var logger = log.Default()

const thumbnailHeight = 400
const slideHeight = 2000
const tileMinDimension = 4100

func main() {
	flag.Parse()
	if len(flag.Args()) != 1 {
		panic("Must provide a directory")
	}
	dir := flag.Args()[0]
	skipFileNames := []string{".DS_Store", "thumbnail", "display", "html", "dzi", "json", "xml"}
	//imageFileSuffixes := []string{".png", ".jpg", ".jpeg"}
	//stripDimsRegex := regexp.MustCompile(`-\d{4,5} x \d{4,5}`)

	var currentDir string
	var err error
	var imageDataMap = map[string]map[string]*ImageData{}

	vips.Startup(nil)
	vips.LoggingSettings(nil, vips.LogLevelMessage)
	defer vips.Shutdown()

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		for _, skipFileName := range skipFileNames {
			// don't process non-images or already generated images
			if strings.Contains(d.Name(), skipFileName) {
				return nil
			}
		}
		logger.Println(path)

		if d.IsDir() {
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

				err := convertToJPG(imageName, currentDir, image)
				if err != nil {
					return err
				}
				ext = ".jpg"
			}

			thumbnailPath := filepath.Join(currentDir, imageName+"-thumbnail"+ext)
			displayPath := filepath.Join(currentDir, imageName+"-display"+ext)
			fullPath := filepath.Join(currentDir, imageName+ext)

			imageData.ThumbPath = thumbnailPath // imageName + "-thumbnail" + ext
			imageData.DisplayPath = displayPath // imageName + "-display" + ext
			imageData.FullPath = fullPath       // imageName + ext

			imageData.Height = image.Height()
			imageData.Width = image.Width()

			// all appropriate images should be jpg, skip anything else
			if ext == ".jpg" {
				processImage(image, imageName, imageData, currentDir)
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

func convertToJPG(imageName string, currentDir string, image *vips.ImageRef) error {
	ext := ".jpg"
	// vips image to jpg
	imageFile := fmt.Sprintf("%s%s", imageName, ext)
	path := filepath.Join(currentDir, imageFile)

	// for web viewing/consistency with generated tiles
	err := image.ToColorSpace(vips.InterpretationSRGB)
	if err != nil {
		return err
	}

	jpgImageBytes, _, err := image.ExportJpeg(vips.NewJpegExportParams())
	if err != nil {
		return err
	}

	err = os.WriteFile(path, jpgImageBytes, 0644)
	if err != nil {
		return err
	}
	return nil
}

func processImage(image *vips.ImageRef, imageName string, imageData ImageData, currentDir string) {
	var display *vips.ImageRef

	jpgExportParams := vips.NewJpegExportParams()
	jpgExportParams.Quality = 75
	jpgExportParams.Interlace = true
	jpgExportParams.OptimizeCoding = true
	jpgExportParams.SubsampleMode = vips.VipsForeignSubsampleAuto
	jpgExportParams.TrellisQuant = true
	jpgExportParams.OvershootDeringing = true
	jpgExportParams.OptimizeScans = true
	jpgExportParams.QuantTable = 3

	// the grid thumbnail
	err := generateThumbnail(imageData.FullPath, jpgExportParams, imageData.ThumbPath)
	if err != nil {
		panic(err)
	}

	// the slide image
	if image.Width() > slideHeight || image.Height() > slideHeight {
		display, err = generateSlideImage(imageData.FullPath, jpgExportParams, imageData.DisplayPath)
		if err != nil {
			panic(err)
		}
	}

	// generate tiles if necessary
	if image.Width() > tileMinDimension || image.Height() > tileMinDimension {
		generateImageTiles(imageName, imageData, display, imageData.FullPath, currentDir)
	}
}

func generateThumbnail(fullPath string, jpgExportParams *vips.JpegExportParams, thumbnailPath string) error {
	thumbnail, err := vips.NewThumbnailFromFile(fullPath, math.MaxInt16, thumbnailHeight, vips.InterestingNone)
	if err != nil {
		return err
	}
	defer thumbnail.Close()

	thumbnailBytes, _, err := thumbnail.ExportJpeg(jpgExportParams)
	if err != nil {
		return err
	}
	err = os.WriteFile(thumbnailPath, thumbnailBytes, 0644)
	if err != nil {
		return err
	}

	return nil
}

func generateSlideImage(fullPath string, jpgExportParams *vips.JpegExportParams, displayPath string) (*vips.ImageRef, error) {
	display, err := vips.NewThumbnailFromFile(fullPath, math.MaxInt16, slideHeight, vips.InterestingNone)
	if err != nil {
		return nil, err
	}
	defer display.Close()

	displayBytes, _, err := display.ExportJpeg(jpgExportParams)
	if err != nil {
		return nil, err
	}

	err = os.WriteFile(displayPath, displayBytes, 0644)
	if err != nil {
		return nil, err
	}
	return display, nil
}

func generateImageTiles(imageName string, imageData ImageData, display *vips.ImageRef, fullPath string, currentDir string) {
	logger.Printf("Generating tiles for %s", imageName)

	// Update width and height to use slide image size, move full image size to max width and max height
	imageData.MaxWidth = imageData.Width
	imageData.MaxHeight = imageData.Height
	if display != nil {
		imageData.Height = display.Height()
		imageData.Width = display.Width()
	}

	// Shell out because govips doesn't have a dzsave binding
	vipsDzCmd := exec.Command("vips", "dzsave", fullPath, filepath.Join(currentDir, imageName), "--centre")
	err := vipsDzCmd.Run()
	if err != nil {
		panic(err)
	}

	imageData.Tiles = filepath.Join(currentDir, imageName+"_files") // imageName + "_files"

	// delete the unnecessary generated meta files
	err = os.Remove(filepath.Join(currentDir, imageName+".dzi"))
	if err != nil {
		logger.Println(err)
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
