package io.pbflags;

import com.google.protobuf.Descriptors.FileDescriptor;
import java.util.List;

/**
 * SPI for discovering proto file descriptors that contain feature flag definitions.
 *
 * <p>Implementations are discovered via {@link java.util.ServiceLoader}. Each provider returns the
 * {@link FileDescriptor} instances for proto files that define feature messages with the {@code
 * (pbflags.feature)} option.
 */
public interface FlagDescriptorProvider {
  List<FileDescriptor> fileDescriptors();
}
