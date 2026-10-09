package firstmate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

const (
	recordPreviewCharacters = 800
	maxRecordHeaderBytes    = 64 * 1024
)

// RecordLocation describes where an ordinary task record currently lives.
type RecordLocation string

const (
	RecordMissing RecordLocation = "missing"
	RecordPending RecordLocation = "pending"
	RecordHandled RecordLocation = "handled"
)

// RecordInspection is a read-only view of one numbered task message.
type RecordInspection struct {
	State          RecordLocation
	Body           string
	BodyCharacters int
	Truncated      bool
}

// InspectRecord reads a bounded preview without changing retry state.
func InspectRecord(stateDir AbsolutePath, id TaskID, record RecordName) (RecordInspection, error) {
	return inspectRecord(stateDir, id, record, recordPreviewCharacters)
}

// InspectRecordFull reads the complete message body without changing retry state.
func InspectRecordFull(
	stateDir AbsolutePath,
	id TaskID,
	record RecordName,
) (RecordInspection, error) {
	return inspectRecord(stateDir, id, record, 0)
}

func inspectRecord(
	stateDir AbsolutePath,
	id TaskID,
	record RecordName,
	previewLimit int,
) (RecordInspection, error) {
	if stateDir.value == "" {
		return RecordInspection{}, ErrInvalidPath
	}
	if id.value == "" {
		return RecordInspection{}, ErrInvalidTaskID
	}
	if record.value == "" {
		return RecordInspection{}, ErrInvalidRecordName
	}

	stateRoot, err := os.OpenRoot(stateDir.value)
	if err != nil {
		return RecordInspection{}, err
	}
	defer stateRoot.Close()
	inboxRoot, err := openInspectedDirectory(stateRoot, id.value+".inbox")
	if err != nil {
		return RecordInspection{}, err
	}
	if inboxRoot == nil {
		return RecordInspection{State: RecordMissing}, nil
	}
	defer inboxRoot.Close()

	pending, err := openInspectedFile(inboxRoot, record.value)
	if err != nil {
		return RecordInspection{}, err
	}
	if pending != nil {
		defer pending.Close()
	}
	handledRoot, err := openInspectedDirectory(inboxRoot, "handled")
	if err != nil {
		return RecordInspection{}, err
	}
	if handledRoot != nil {
		defer handledRoot.Close()
	}
	var handled *os.File
	if handledRoot != nil {
		handled, err = openInspectedFile(handledRoot, record.value)
		if err != nil {
			return RecordInspection{}, err
		}
		if handled != nil {
			defer handled.Close()
		}
	}

	if pending != nil && handled != nil {
		pendingInfo, pendingErr := pending.Stat()
		handledInfo, handledErr := handled.Stat()
		if pendingErr != nil || handledErr != nil {
			return RecordInspection{}, errors.Join(pendingErr, handledErr)
		}
		if os.SameFile(pendingInfo, handledInfo) {
			return readRecordBody(handled, RecordHandled, previewLimit)
		}
		return RecordInspection{}, fmt.Errorf(
			"%w: record exists in pending and handled", ErrInvalidMetadata,
		)
	}
	if pending != nil {
		return readRecordBody(pending, RecordPending, previewLimit)
	}
	if handled != nil {
		return readRecordBody(handled, RecordHandled, previewLimit)
	}
	return RecordInspection{State: RecordMissing}, nil
}

func openInspectedDirectory(parent *os.Root, name string) (*os.Root, error) {
	selected, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !selected.IsDir() {
		return nil, fmt.Errorf("%w: task inbox path is not a directory", ErrInvalidMetadata)
	}
	opened, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	current, err := opened.Stat(".")
	if err != nil || !os.SameFile(selected, current) {
		_ = opened.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: task inbox directory changed during open", ErrInvalidMetadata)
	}
	return opened, nil
}

func openInspectedFile(parent *os.Root, name string) (*os.File, error) {
	selected, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !selected.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: task record is not regular", ErrInvalidMetadata)
	}
	opened, err := parent.Open(name)
	if err != nil {
		return nil, err
	}
	current, err := opened.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(selected, current) {
		_ = opened.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: task record changed during open", ErrInvalidMetadata)
	}
	return opened, nil
}

func readRecordBody(
	file *os.File,
	state RecordLocation,
	previewLimit int,
) (RecordInspection, error) {
	reader := bufio.NewReaderSize(file, maxRecordHeaderBytes)
	headerBytes := 0
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil || len(line) == 0 {
			return RecordInspection{}, fmt.Errorf(
				"%w: malformed message envelope",
				ErrInvalidMetadata,
			)
		}
		headerBytes += len(line)
		if headerBytes > maxRecordHeaderBytes {
			return RecordInspection{}, fmt.Errorf(
				"%w: message envelope too large",
				ErrInvalidMetadata,
			)
		}
		if headerBytes == len(line) && !bytes.Equal(line, []byte("schema=fm-task-inbox.v1\n")) {
			return RecordInspection{}, fmt.Errorf(
				"%w: unsupported message schema",
				ErrInvalidMetadata,
			)
		}
		if bytes.Equal(line, []byte("--\n")) {
			break
		}
	}
	if previewLimit == 0 {
		body, err := io.ReadAll(reader)
		if err != nil {
			return RecordInspection{}, err
		}
		if !utf8.Valid(body) {
			return RecordInspection{}, fmt.Errorf(
				"%w: message body is not UTF-8",
				ErrInvalidMetadata,
			)
		}
		return RecordInspection{
			State:          state,
			Body:           string(body),
			BodyCharacters: utf8.RuneCount(body),
		}, nil
	}
	var preview []rune
	characters := 0
	for {
		character, size, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return RecordInspection{}, err
		}
		if character == utf8.RuneError && size == 1 {
			return RecordInspection{}, fmt.Errorf(
				"%w: message body is not UTF-8",
				ErrInvalidMetadata,
			)
		}
		if characters < previewLimit {
			preview = append(preview, character)
		}
		characters++
	}
	return RecordInspection{
		State: state, Body: string(preview), BodyCharacters: characters,
		Truncated: characters > previewLimit,
	}, nil
}
